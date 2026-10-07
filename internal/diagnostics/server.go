// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package diagnostics serves read-only process and Ara health information.
package diagnostics

import (
	"bufio"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/csv"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/starfederation/datastar-go/datastar"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Access configures the optional diagnostics listener. Remote access must use TLS and Basic auth.
type Access struct {
	Listen   string
	Username string
	Password string
	TLSCert  string
	TLSKey   string
	Pprof    bool
}

// Validate enforces loopback-by-default access and remote TLS/authentication.
func (a Access) Validate() error {
	if a.Pprof && a.Listen == "" {
		return errors.New("diagnostics profiling requires a diagnostics listener")
	}
	if a.Pprof && (a.Username == "" || a.Password == "") {
		return errors.New("diagnostics profiling requires basic authentication")
	}
	if (a.Username == "") != (a.Password == "") {
		return errors.New("diagnostics username and password must be configured together")
	}
	if (a.TLSCert == "") != (a.TLSKey == "") {
		return errors.New("diagnostics TLS certificate and key must be configured together")
	}
	if a.Listen == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return errors.New("diagnostics listen address must be host:port")
	}
	if port == "" {
		return errors.New("diagnostics listen port is required")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("diagnostics listen port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && (a.Username == "" || a.Password == "" || a.TLSCert == "" || a.TLSKey == "") {
		return errors.New("remote diagnostics require basic authentication and TLS certificate/key")
	}
	return nil
}

// Runtime holds dependencies used by the diagnostics listener.
type Runtime struct {
	Ara             *ara.Client
	Sampler         *monitor.Sampler
	Logger          *slog.Logger
	Version         string
	StartedAt       time.Time
	Metrics         http.Handler
	Meter           metric.Meter
	ExportLimit     int
	Archive         *monitor.Archive
	RecentAraEvents func() AraEventSnapshot
}

// AraEventSnapshot is the read-only view of events from an existing owned session socket.
type AraEventSnapshot struct {
	Available    bool                 `json:"available"`
	Stale        bool                 `json:"stale"`
	Gap          bool                 `json:"gap"`
	LastSequence int64                `json:"last_sequence"`
	Dropped      int64                `json:"dropped"`
	Events       []ara.WebSocketEvent `json:"events"`
}

//go:embed assets/datastar-0.21.4.js
var datastarRuntime []byte

type dashboardMetrics struct {
	exports           metric.Int64Counter
	activeExports     metric.Int64UpDownCounter
	exportDuration    metric.Float64Histogram
	subscribers       metric.Int64UpDownCounter
	subscriberRejects metric.Int64Counter
	streams           metric.Int64Counter
}

const dashboardEventLimit = 50

func newDashboardMetrics(meter metric.Meter) (dashboardMetrics, error) {
	var result dashboardMetrics
	var err error
	if result.exports, err = meter.Int64Counter("resource.dashboard.exports", metric.WithUnit("{export}"), metric.WithDescription("Dashboard exports by format and outcome")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create dashboard export counter: %w", err)
	}
	if result.activeExports, err = meter.Int64UpDownCounter("resource.dashboard.active_exports", metric.WithUnit("{export}"), metric.WithDescription("Currently active dashboard exports by format")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create active dashboard export counter: %w", err)
	}
	if result.exportDuration, err = meter.Float64Histogram("resource.dashboard.export.duration", metric.WithUnit("s"), metric.WithDescription("Dashboard export request duration by format and outcome")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create dashboard export duration: %w", err)
	}
	if result.subscribers, err = meter.Int64UpDownCounter("resource.dashboard.subscribers", metric.WithUnit("{subscriber}"), metric.WithDescription("Connected resource dashboard streams")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create dashboard subscriber counter: %w", err)
	}
	if result.subscriberRejects, err = meter.Int64Counter("resource.dashboard.subscriber.rejections", metric.WithUnit("{rejection}"), metric.WithDescription("Dashboard streams rejected by the subscriber limit")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create dashboard subscriber rejection counter: %w", err)
	}
	if result.streams, err = meter.Int64Counter("resource.dashboard.streams", metric.WithUnit("{stream}"), metric.WithDescription("Dashboard SSE stream terminations by bounded outcome")); err != nil {
		return dashboardMetrics{}, fmt.Errorf("create dashboard stream counter: %w", err)
	}
	return result, nil
}

type exportSample struct {
	SchemaVersion  string    `json:"schema_version"`
	InstanceID     string    `json:"instance_id"`
	SampleSequence uint64    `json:"sample_sequence"`
	SampledAt      time.Time `json:"sampled_at"`
	UptimeSeconds  float64   `json:"uptime_seconds"`
	CPUPercent     *float64  `json:"cpu_percent"`
	LogicalCPUs    int       `json:"logical_cpus"`
	RSSBytes       *uint64   `json:"rss_bytes"`
	GoAllocBytes   uint64    `json:"go_alloc_bytes"`
	GoSysBytes     uint64    `json:"go_sys_bytes"`
	Goroutines     int       `json:"goroutines"`
	GOMaxProcs     int       `json:"gomaxprocs"`
	HeapObjects    uint64    `json:"heap_objects"`
	GCCount        uint32    `json:"gc_count"`
}

func resourceSample(sample monitor.Snapshot) exportSample {
	result := exportSample{SchemaVersion: sample.SchemaVersion, InstanceID: sample.InstanceID, SampleSequence: sample.SampleSequence, SampledAt: sample.SampledAt, UptimeSeconds: sample.UptimeSeconds, LogicalCPUs: sample.LogicalCPUs, GoAllocBytes: sample.GoAllocBytes, GoSysBytes: sample.GoSysBytes, Goroutines: sample.Goroutines, GOMaxProcs: sample.GOMaxProcs, HeapObjects: sample.HeapObjects, GCCount: sample.GCCount}
	if sample.CPUAvailable {
		result.CPUPercent = &sample.CPUPercent
	}
	if sample.RSSAvailable {
		result.RSSBytes = &sample.RSSBytes
	}
	return result
}

const dashboardStyles = `
:root{color-scheme:dark;--ink:#edf5ff;--muted:#9aa9c4;--panel:#111a31e8;--line:#273653;--cyan:#5ce1e6;--violet:#a98bff;--gold:#ffcf70;--track:#283653}
*{box-sizing:border-box}
body{margin:0;min-height:100vh;color:var(--ink);font:15px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;background:radial-gradient(ellipse at 12% 4%,#23366d88,transparent 38rem),radial-gradient(ellipse at 94% 20%,#542c6b66,transparent 34rem),linear-gradient(160deg,#080d1c,#050814 60%,#0b1225);background-attachment:fixed}
body::before{position:fixed;inset:0;z-index:0;pointer-events:none;content:"";opacity:.34;background-image:radial-gradient(1px 1px at 8% 18%,#fff 99%,transparent),radial-gradient(1px 1px at 28% 72%,#b8d5ff 99%,transparent),radial-gradient(1.5px 1.5px at 53% 12%,#fff 99%,transparent),radial-gradient(1px 1px at 76% 44%,#fff 99%,transparent),radial-gradient(1px 1px at 91% 83%,#c9d8ff 99%,transparent);background-size:390px 310px;animation:star-drift 90s linear infinite}
@keyframes star-drift{to{background-position:390px 310px}}
#stream{display:none}
.page-shell{position:relative;z-index:1;width:min(1120px,calc(100% - 32px));margin:0 auto;padding:42px 0 54px}
.masthead{display:flex;justify-content:space-between;align-items:flex-start;gap:20px;margin-bottom:26px}
.eyebrow{margin:0 0 7px;color:var(--cyan);font-size:.75rem;font-weight:750;letter-spacing:.18em;text-transform:uppercase}
h1{margin:0;font-size:clamp(2rem,5vw,3rem);line-height:1.08;letter-spacing:-.04em}
.subtitle{margin:10px 0 0;color:var(--muted)}
.live-pill{display:inline-flex;align-items:center;gap:9px;padding:8px 12px;border:1px solid #31555e;border-radius:999px;background:#0d202a;color:#aaf5e5;white-space:nowrap;font-size:.82rem}
.live-dot{width:8px;height:8px;border-radius:50%;background:#69f0c3;box-shadow:0 0 12px #69f0c3}
.status{min-height:24px;margin:0 0 18px;color:var(--muted);font-size:.88rem}
.metric-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px;margin-bottom:16px}
.panel{border:1px solid #263653;border-radius:18px;background:linear-gradient(145deg,#131e38f2,#0c1428f2);box-shadow:0 16px 48px #0005, inset 0 1px #ffffff08}
.metric-card{display:flex;align-items:center;justify-content:space-between;gap:20px;min-height:220px;padding:22px 26px;overflow:hidden;position:relative}
.metric-card::after{position:absolute;right:-55px;bottom:-105px;width:230px;height:230px;border:1px solid #ffffff0b;border-radius:50%;box-shadow:0 0 0 22px #ffffff04,0 0 0 48px #ffffff03;content:"";pointer-events:none}
.metric-label{margin:0;color:var(--muted);font-size:.78rem;font-weight:700;letter-spacing:.12em;text-transform:uppercase}
.metric-value{display:block;margin-top:9px;font-size:clamp(1.6rem,4vw,2.3rem);font-variant-numeric:tabular-nums;letter-spacing:-.04em}
.metric-note{max-width:250px;margin:6px 0 0;color:var(--muted);font-size:.82rem}
.gauge{--fill:0%;position:relative;display:grid;flex:0 0 132px;place-items:center;width:132px;aspect-ratio:1;border-radius:50%;background:conic-gradient(var(--gauge-color,var(--cyan)) var(--fill),var(--track) 0);filter:drop-shadow(0 0 18px color-mix(in srgb,var(--gauge-color,var(--cyan)) 22%,transparent))}
.gauge::before{position:absolute;inset:10px;border:1px solid #ffffff12;border-radius:50%;background:#101a30;content:""}
.gauge-core{position:relative;display:flex;flex-direction:column;align-items:center;justify-content:center;width:100%;height:100%;text-align:center}
.gauge-core strong{max-width:112px;overflow:hidden;font-size:1.55rem;font-variant-numeric:tabular-nums;letter-spacing:-.04em;text-overflow:ellipsis;white-space:nowrap}
.gauge-core span{color:var(--muted);font-size:.7rem;letter-spacing:.08em;text-transform:uppercase}
.memory-card{--gauge-color:var(--violet)}
.chart-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}
.chart-card{min-width:0;padding:20px 22px 16px}
.chart-head{display:flex;justify-content:space-between;align-items:flex-start;gap:12px;margin-bottom:12px}
.chart-head h2{margin:0;font-size:1rem;letter-spacing:.01em}
.chart-head p{margin:4px 0 0;color:var(--muted);font-size:.78rem}
.chart-current{color:var(--ink);font-size:.9rem;font-variant-numeric:tabular-nums;white-space:nowrap}
.chart{display:block;width:100%;height:auto;overflow:visible}
.chart-gridline{stroke:#293755;stroke-dasharray:3 7;stroke-width:1}
.chart-axis{fill:#8797b5;font:11px system-ui,sans-serif}
.chart-line{fill:none;stroke:var(--cyan);stroke-linecap:round;stroke-linejoin:round;stroke-width:3;filter:drop-shadow(0 0 5px #5ce1e677)}
.memory-line{stroke:var(--violet);filter:drop-shadow(0 0 5px #a98bff77)}
.chart-dot{fill:#e8ffff;stroke:var(--cyan);stroke-width:3}.memory-dot{stroke:var(--violet)}
.chart-foot{display:flex;justify-content:space-between;gap:8px;margin:4px 0 0;color:var(--muted);font-size:.72rem;font-variant-numeric:tabular-nums}
.history-note{margin:17px 0 0;color:var(--muted);font-size:.78rem;text-align:center}
.event-panel{margin-top:18px;overflow:hidden}
.event-head{display:flex;justify-content:space-between;align-items:center;gap:14px;padding:19px 22px 14px}
.event-head h2{margin:0;font-size:1rem}
.event-state{color:var(--muted);font-size:.78rem;text-align:right}
.event-note{margin:0;padding:0 22px 14px;color:var(--muted);font-size:.78rem}
.event-note.gap{color:var(--gold)}
.event-table-wrap{max-height:340px;overflow:auto;border-top:1px solid var(--line)}
.event-table{width:100%;border-collapse:collapse;text-align:left;font-size:.82rem}
.event-table th{position:sticky;top:0;z-index:1;background:#101a30;color:var(--muted);font-size:.7rem;letter-spacing:.1em;text-transform:uppercase}
.event-table th,.event-table td{padding:10px 16px;border-bottom:1px solid #263653}
.event-table tbody tr:last-child td{border-bottom:0}
.event-table tbody tr:first-child td{color:#e7faff}
.event-time{min-width:145px;color:var(--muted);font-variant-numeric:tabular-nums;white-space:nowrap}
.event-sequence{width:90px;color:var(--cyan);font-variant-numeric:tabular-nums}
.event-type{min-width:150px;color:#c8b5ff;font-family:ui-monospace,SFMono-Regular,Consolas,monospace}
.event-failure .event-type,.event-failure td:last-child{color:#ff9b9b}
.event-empty{color:var(--muted);text-align:center}
.downloads{margin:23px 0 0;text-align:center;color:var(--muted);font-size:.85rem}
a{color:#a8dfff;text-decoration:none}a:hover{text-decoration:underline}a:focus-visible{outline:2px solid var(--gold);outline-offset:3px;border-radius:3px}
@media(max-width:760px){.page-shell{width:min(100% - 22px,600px);padding-top:28px}.metric-grid,.chart-grid{grid-template-columns:1fr}.metric-card{min-height:190px}.masthead{align-items:center}.live-pill{padding:7px 9px;font-size:.72rem}}
@media(max-width:420px){.metric-card{padding:18px;gap:10px}.gauge{flex-basis:112px;width:112px}.gauge-core strong{font-size:1.3rem}.chart-card{padding:16px 12px}.masthead{align-items:flex-start;flex-direction:column}}
@media(prefers-reduced-motion:reduce){*,*::before,*::after{scroll-behavior:auto!important;animation-duration:.01ms!important;animation-iteration-count:1!important;transition-duration:.01ms!important}}
`

const dashboardScript = `
(()=>{
  const history=[];
  const historyLimit=60;
  let lastRaw="",lastInstance="",lastSequence=0;
  const byId=(id)=>document.getElementById(id);
  const mib=(bytes)=>(bytes/1048576).toFixed(1)+" MiB";
  const localTime=(value)=>new Intl.DateTimeFormat(undefined,{hour:"2-digit",minute:"2-digit",second:"2-digit"}).format(new Date(value));
  const eventTime=(value)=>{const date=new Date(value);return value&&Number.isFinite(date.valueOf())?date.toLocaleString():"—";};
  const numeric=(value)=>typeof value==="number"&&Number.isFinite(value);
  function gauge(id,valueId,value,capacity,display,accessible){
    const node=byId(id),text=byId(valueId);
    if(!node||!text)return;
    const available=numeric(value),fill=available&&capacity>0?Math.max(0,Math.min(100,value/capacity*100)):0;
    node.style.setProperty("--fill",fill+"%");
    node.setAttribute("aria-valuenow",String(Math.round(fill)));
    node.setAttribute("aria-valuetext",available?accessible(value):"Unavailable");
    text.textContent=available?display(value):"—";
  }
  function chart(kind,field,base,format){
    const line=byId(kind+"-line"),dot=byId(kind+"-dot"),high=byId(kind+"-axis-high"),middle=byId(kind+"-axis-middle");
    const values=history.map((sample)=>numeric(sample[field])?sample[field]:null);
    const max=Math.max(base,...values.filter(numeric));
    let path="",connected=false,lastPoint=null;
    values.forEach((value,index)=>{
      if(value===null){connected=false;return;}
      const x=54+(history.length<2?556:index/(history.length-1)*556);
      const y=156-(value/max)*132;
      path+=(connected?" L ":"M ")+x.toFixed(1)+" "+y.toFixed(1);
      connected=true;lastPoint=[x,y];
    });
    if(line)line.setAttribute("d",path);
    if(dot&&lastPoint){dot.setAttribute("cx",lastPoint[0]);dot.setAttribute("cy",lastPoint[1]);dot.setAttribute("opacity","1");}else if(dot)dot.setAttribute("opacity","0");
    if(high)high.textContent=format(max);
    if(middle)middle.textContent=format(max/2);
    return max;
  }
  function freshness(sample){
    const node=byId("freshness");
    if(!node||!sample)return;
    const age=Date.now()-Date.parse(sample.sampled_at);
    if(age>5000){node.textContent="Stale · no sample received for "+Math.floor(age/1000)+" seconds";return;}
    node.textContent=(sample.gap?"History gap or process restart · ":"Live sample · ")+localTime(sample.sampled_at)+" local time";
  }
  function render(){
    if(!history.length)return;
    const current=history[history.length-1];
    const cpu=numeric(current.cpu_percent)?current.cpu_percent:null;
    const memory=numeric(current.rss_bytes)?current.rss_bytes:null;
    const peak=history.reduce((max,sample)=>numeric(sample.rss_bytes)?Math.max(max,sample.rss_bytes):max,0);
    gauge("cpu-gauge","cpu-value",cpu,100,(value)=>value.toFixed(1)+"%",(value)=>value.toFixed(1)+"% of one logical core; ring caps at 100%");
    gauge("memory-gauge","memory-value",memory,peak,mib,(value)=>mib(value)+" RSS; ring is relative to the visible history peak");
    const cpus=byId("cpu-context");if(cpus)cpus.textContent=current.logical_cpus+" logical CPUs · one core = 100%";
    const peakLabel=byId("memory-context");if(peakLabel)peakLabel.textContent="Ring scale: visible-window peak "+(peak?mib(peak):"—");
    chart("cpu","cpu_percent",100,(value)=>value.toFixed(0)+"%");
    chart("memory","rss_bytes",1,mib);
    const count=byId("history-count");if(count)count.textContent=history.length+" / "+historyLimit+" recent samples";
    const range=byId("history-range");if(range)range.textContent=localTime(history[0].sampled_at)+" — "+localTime(current.sampled_at);
    const cpuNow=byId("cpu-chart-current");if(cpuNow)cpuNow.textContent=cpu===null?"Unavailable":cpu.toFixed(1)+"%";
    const memoryNow=byId("memory-chart-current");if(memoryNow)memoryNow.textContent=memory===null?"Unavailable":mib(memory);
    const cpuValue=byId("cpu-current");if(cpuValue)cpuValue.textContent=cpu===null?"Unavailable":cpu.toFixed(1)+"%";
    const memoryValue=byId("memory-current");if(memoryValue)memoryValue.textContent=memory===null?"Unavailable":mib(memory);
    freshness(current);
  }
  function ingest(){
    const source=byId("sample-data"),raw=source&&source.dataset.sample;
    if(!raw||raw===lastRaw)return;
    let sample;
    try{sample=JSON.parse(raw);}catch(_){return;}
    const sequence=Number(sample.sample_sequence),gap=source.dataset.gap==="true";
    if(history.length&&(gap||sample.instance_id!==lastInstance||sequence!==lastSequence+1))history.length=0;
    sample.gap=gap;history.push(sample);
    if(history.length>historyLimit)history.splice(0,history.length-historyLimit);
    lastRaw=raw;lastInstance=sample.instance_id;lastSequence=sequence;render();
  }
  const araEventRows=[],eventLimit=50;
  let lastEventRaw="",lastEventSequence=0,lastControlAvailable=false,araEventStatus={};
  function eventDetails(event){
    const details=[];
    if(event.device_name)details.push((event.device_type?event.device_type+" ":"equipment ")+event.device_name);
    else if(event.device_type)details.push(event.device_type);
    if(event.device_id)details.push("id "+event.device_id);
    if(event.removed)details.push("device removed");
    if(event.state)details.push("state "+event.state);
    if(event.kind)details.push("kind "+event.kind);
    if(event.action)details.push("action "+event.action);
    if(event.details)details.push(event.details);
    if(event.detected_utc)details.push("detected "+eventTime(event.detected_utc));
    if(event.sequence_id)details.push("sequence "+event.sequence_id);
    if(event.run_id)details.push("run "+event.run_id);
    if(event.job_id)details.push("job "+event.job_id);
    if(event.frame_id)details.push("frame "+event.frame_id);
    if(event.instructions_total>0)details.push("instructions "+event.instructions_completed+"/"+event.instructions_total);
    if(event.current_instruction_index!==undefined)details.push("item "+event.current_instruction_index);
    if(event.failed_instruction_index!==undefined)details.push("failed item "+event.failed_instruction_index+(event.failed_instruction_name?" · "+event.failed_instruction_name:""));
    else if(event.failed_instruction_name)details.push("failed item "+event.failed_instruction_name);
    if(event.failure_reason)details.push(event.failure_reason);
    if(event.exposure){
      const exposure=event.exposure;
      if(exposure.exposure_sec!==undefined)details.push("exposure "+exposure.exposure_sec+" s");
      if(exposure.filter_name)details.push("filter "+exposure.filter_name);
      if(exposure.elapsed_ms!==undefined)details.push("elapsed "+exposure.elapsed_ms+" ms");
      if(exposure.reason)details.push(exposure.reason);
    }
    if(event.guider){
      const guider=event.guider;
      if(guider.frame!==undefined)details.push("guide frame "+guider.frame);
      if(guider.ra_arcsec!==undefined||guider.dec_arcsec!==undefined)details.push("guide error "+(guider.ra_arcsec??"—")+" / "+(guider.dec_arcsec??"—")+" arcsec");
      else if(guider.ra_raw_px!==undefined||guider.dec_raw_px!==undefined)details.push("guide offset "+(guider.ra_raw_px??"—")+" / "+(guider.dec_raw_px??"—")+" px");
      if(guider.ra_duration_ms!==undefined||guider.dec_duration_ms!==undefined)details.push("pulse "+(guider.ra_duration_ms??"—")+" / "+(guider.dec_duration_ms??"—")+" ms");
      if(guider.snr!==undefined)details.push("SNR "+guider.snr);
      if(guider.star_mass!==undefined)details.push("star mass "+guider.star_mass);
      if(guider.pixel_scale_arcsec!==undefined)details.push("scale "+guider.pixel_scale_arcsec+" arcsec/px");
      if(guider.dx_px!==undefined||guider.dy_px!==undefined)details.push("marker Δ "+(guider.dx_px??"—")+" / "+(guider.dy_px??"—")+" px");
      if(guider.distance_px!==undefined)details.push("distance "+guider.distance_px+" px");
      if(guider.settle_time_sec!==undefined)details.push("settle "+guider.settle_time_sec+" s");
      if(guider.error)details.push(guider.error);
    }
    if(event.autofocus){
      const autofocus=event.autofocus;
      if(autofocus.mode||autofocus.phase)details.push("focus "+[autofocus.mode,autofocus.phase].filter(Boolean).join(" / "));
      if(autofocus.step_index!==undefined)details.push("probe "+autofocus.step_index+" / "+(autofocus.total_steps??"?"));
      if(autofocus.shot_index!==undefined)details.push("shot "+autofocus.shot_index);
      if(autofocus.position!==undefined)details.push("position "+autofocus.position);
      if(autofocus.hfr!==undefined)details.push("HFR "+autofocus.hfr);
      if(autofocus.stars!==undefined)details.push("stars "+autofocus.stars);
      if(autofocus.total_steps!==undefined&&autofocus.step_index===undefined)details.push("steps "+autofocus.total_steps);
      if(autofocus.stars_used!==undefined)details.push("stars "+autofocus.stars_used);
      if(autofocus.final_position!==undefined)details.push("final position "+autofocus.final_position);
      if(autofocus.final_hfr!==undefined)details.push("final HFR "+autofocus.final_hfr);
      if(autofocus.final_stars!==undefined)details.push("final stars "+autofocus.final_stars);
      if(autofocus.duration_seconds!==undefined)details.push("duration "+autofocus.duration_seconds+" s");
      if(autofocus.kept===false)details.push("probe dropped");
      if(autofocus.reason)details.push(autofocus.reason);
      if(autofocus.algorithm)details.push("fit "+autofocus.algorithm+" · R² "+(autofocus.r_squared??"—"));
      if(autofocus.best_position!==undefined)details.push("best position "+autofocus.best_position);
      if(autofocus.predicted_hfr!==undefined)details.push("predicted HFR "+autofocus.predicted_hfr);
      if(autofocus.usable===false)details.push("fit unusable");
      if(autofocus.within_range===false)details.push("best fit outside sampled range");
      if(autofocus.severity)details.push("collimation "+autofocus.severity);
      if(autofocus.offset_percent!==undefined)details.push("offset "+autofocus.offset_percent+"%");
      if(autofocus.direction_degrees!==undefined)details.push("direction "+autofocus.direction_degrees+"°");
    }
    return details.join(" · ")||"—";
  }
  function eventCell(row,value,className){
    const cell=document.createElement("td");
    cell.textContent=String(value===undefined||value===null?"":value);
    if(className)cell.className=className;
    row.append(cell);
  }
  function renderAraEvents(){
    const body=byId("ara-event-rows");if(!body)return;
    body.replaceChildren();
    if(!araEventRows.length){
      const row=document.createElement("tr"),cell=document.createElement("td");
      cell.colSpan=3;cell.className="event-empty";
      cell.textContent=araEventStatus.available?"Waiting for Ara events":"No recent events from an owned Ara control session";
      row.append(cell);body.append(row);
    }else{
      for(const event of araEventRows){
        const row=document.createElement("tr"),failed=Boolean(event.failure_reason)||/(failed|error|failure)/i.test(String(event.type||""));
        if(failed)row.className="event-failure";
        eventCell(row,eventTime(event.ts),"event-time");
        eventCell(row,event.seq,"event-sequence");
        eventCell(row,event.type||"unknown","event-type");
        eventCell(row,eventDetails(event),"");
        body.append(row);
      }
    }
    const state=byId("ara-event-state");
    if(state)state.textContent=araEventStatus.available?(araEventStatus.stale?"Owned session · reconnecting or stale":"Owned session · live"):(araEventRows.length?"Session inactive · retained events":"Waiting for an owned session");
    const note=byId("ara-event-note");
    if(note){
      note.classList.toggle("gap",Boolean(araEventStatus.gap));
      note.textContent=araEventStatus.gap?"Event gap detected · "+(araEventStatus.dropped||0)+" dropped · reconcile with Ara state tools":"Events come only from ara-mcp's existing owned Ara session socket; this page opens no WebSocket.";
    }
    const count=byId("ara-event-count");if(count)count.textContent=araEventRows.length+" / "+eventLimit+" recent events";
  }
  function ingestAraEvents(){
    const source=byId("event-data"),raw=source&&source.dataset.eventUpdate;
    if(raw&&raw!==lastEventRaw){
      let update;
      try{update=JSON.parse(raw);}catch(_){return;}
      if(update.available&&!lastControlAvailable){araEventRows.length=0;lastEventSequence=0;}
      const events=Array.isArray(update.events)?update.events:[];
      for(const event of events){
        const sequence=Number(event.seq);
        if(!Number.isFinite(sequence)||sequence<=lastEventSequence)continue;
        araEventRows.unshift(event);lastEventSequence=sequence;
        if(araEventRows.length>eventLimit)araEventRows.pop();
      }
      araEventStatus=update;lastControlAvailable=Boolean(update.available);lastEventRaw=raw;
    }
    renderAraEvents();
  }
  function updateDashboard(){ingest();ingestAraEvents();}
  new MutationObserver(updateDashboard).observe(document.body,{subtree:true,attributes:true,attributeFilter:["data-sample","data-gap","data-event-update"]});
  updateDashboard();
  setInterval(()=>freshness(history[history.length-1]),1000);
})();
`

func dashboardFragment(sample monitor.Snapshot, gap bool, araEvents AraEventSnapshot, newAraEvents []ara.WebSocketEvent) (string, error) {
	data, err := json.Marshal(resourceSample(sample))
	if err != nil {
		return "", fmt.Errorf("marshal dashboard sample: %w", err)
	}
	araEvents.Events = newAraEvents
	eventData, err := json.Marshal(araEvents)
	if err != nil {
		return "", fmt.Errorf("marshal Ara dashboard events: %w", err)
	}
	sampledAt := sample.SampledAt.UTC().Format(time.RFC3339Nano)
	status := "Last sample: " + sampledAt
	if gap {
		status = "A history gap or process restart was detected; showing latest sample: " + sampledAt
	}
	fragment := `<main id="dashboard" class="page-shell"><header class="masthead"><div><p class="eyebrow">Ara observatory · live telemetry</p><h1>OpenAstro Ara MCP</h1><p class="subtitle">A live view of the ara-mcp process</p></div><span class="live-pill"><span class="live-dot" aria-hidden="true"></span>Local process</span></header>
<p id="freshness" class="status" data-sampled-at="` + html.EscapeString(sampledAt) + `" aria-live="polite">` + html.EscapeString(status) + `</p>
<div class="metric-grid">
<article class="panel metric-card"><div><p class="metric-label">CPU · one core = 100%</p><strong id="cpu-current" class="metric-value">—</strong><p id="cpu-context" class="metric-note">Waiting for sample</p><p class="metric-note">Ring caps at 100%; value and chart can exceed it.</p></div><div id="cpu-gauge" class="gauge" role="meter" aria-label="CPU usage gauge" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0" aria-valuetext="Waiting for sample"><div class="gauge-core"><strong id="cpu-value">—</strong><span>CPU</span></div></div></article>
<article class="panel metric-card memory-card"><div><p class="metric-label">Resident memory · RSS</p><strong id="memory-current" class="metric-value">—</strong><p id="memory-context" class="metric-note">Ring scale: visible-window peak</p><p class="metric-note">RSS is process memory, not Go heap or system capacity.</p></div><div id="memory-gauge" class="gauge" role="meter" aria-label="RSS memory usage gauge" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0" aria-valuetext="Waiting for sample"><div class="gauge-core"><strong id="memory-value">—</strong><span>RSS</span></div></div></article>
</div>
<div class="chart-grid">
<section class="panel chart-card"><div class="chart-head"><div><h2>CPU usage over time</h2><p>Percent of one logical core</p></div><output id="cpu-chart-current" class="chart-current">—</output></div><svg class="chart" viewBox="0 0 620 180" role="img" aria-label="CPU usage over time"><path class="chart-gridline" d="M54 24H610 M54 68H610 M54 112H610 M54 156H610"/><text id="cpu-axis-high" class="chart-axis" x="2" y="28">100%</text><text id="cpu-axis-middle" class="chart-axis" x="2" y="72">50%</text><text class="chart-axis" x="28" y="160">0%</text><path id="cpu-line" class="chart-line" d=""/><circle id="cpu-dot" class="chart-dot" cx="54" cy="156" r="4" opacity="0"/></svg><p id="history-range" class="chart-foot"><span>Waiting for samples</span><span>Recent samples</span></p></section>
<section class="panel chart-card"><div class="chart-head"><div><h2>Memory usage over time</h2><p>Resident set size · MiB</p></div><output id="memory-chart-current" class="chart-current">—</output></div><svg class="chart" viewBox="0 0 620 180" role="img" aria-label="Memory usage over time"><path class="chart-gridline" d="M54 24H610 M54 68H610 M54 112H610 M54 156H610"/><text id="memory-axis-high" class="chart-axis" x="2" y="28">—</text><text id="memory-axis-middle" class="chart-axis" x="2" y="72">—</text><text class="chart-axis" x="28" y="160">0</text><path id="memory-line" class="chart-line memory-line" d=""/><circle id="memory-dot" class="chart-dot memory-dot" cx="54" cy="156" r="4" opacity="0"/></svg><p class="chart-foot"><span>Older</span><span>Now</span></p></section>
</div>
<p id="history-count" class="history-note" aria-live="polite">Waiting for samples</p>
<section class="panel event-panel" aria-label="Ara server events"><div class="event-head"><h2>Ara server events</h2><span id="ara-event-state" class="event-state">Waiting for an owned session</span></div><p id="ara-event-note" class="event-note">Events come only from ara-mcp's existing owned Ara session socket; this page opens no WebSocket.</p><div class="event-table-wrap"><table class="event-table" aria-label="Ara server events"><thead><tr><th scope="col">Time</th><th scope="col">Sequence</th><th scope="col">Event</th><th scope="col">Details</th></tr></thead><tbody id="ara-event-rows" aria-live="polite"><tr><td colspan="4" class="event-empty">No recent events from an owned Ara control session</td></tr></tbody></table></div><p id="ara-event-count" class="event-note">0 / 50 recent events · Newest first</p></section>
<div id="sample-data" hidden data-gap="` + strconv.FormatBool(gap) + `" data-sample="` + html.EscapeString(string(data)) + `"></div>
<div id="event-data" hidden data-event-update="` + html.EscapeString(string(eventData)) + `"></div>
<p class="downloads"><a href="/resources.csv">Download CSV</a> · <a href="/resources.jsonl">Download JSONL</a></p></main>`
	return strings.ReplaceAll(fragment, "\n", ""), nil
}

func parseDashboardCursor(value string) (string, uint64, bool, bool) {
	if value == "" {
		return "", 0, false, false
	}
	instance, rawSequence, ok := strings.Cut(value, ":")
	if !ok || instance == "" {
		return "", 0, true, false
	}
	sequence, err := strconv.ParseUint(rawSequence, 10, 64)
	return instance, sequence, true, err == nil
}

func lastDashboardEvents(events []ara.WebSocketEvent) []ara.WebSocketEvent {
	if len(events) > dashboardEventLimit {
		return events[len(events)-dashboardEventLimit:]
	}
	return events
}

type dashboardAraEventCursor struct {
	lastSequence  int64
	lastAvailable bool
	initialized   bool
}

func (cursor *dashboardAraEventCursor) advance(snapshot AraEventSnapshot) []ara.WebSocketEvent {
	var events []ara.WebSocketEvent
	if !cursor.initialized || (snapshot.Available && !cursor.lastAvailable) {
		cursor.lastSequence = 0
		events = lastDashboardEvents(snapshot.Events)
	} else {
		for _, event := range snapshot.Events {
			if event.Seq > cursor.lastSequence {
				events = append(events, event)
				cursor.lastSequence = event.Seq
			}
		}
	}
	for _, event := range events {
		if event.Seq > cursor.lastSequence {
			cursor.lastSequence = event.Seq
		}
	}
	if snapshot.LastSequence > cursor.lastSequence {
		cursor.lastSequence = snapshot.LastSequence
	}
	cursor.lastAvailable, cursor.initialized = snapshot.Available, true
	return events
}

// NewMetrics creates an isolated Prometheus registry and an OpenTelemetry reader for it.
func NewMetrics() (*sdkmetric.MeterProvider, http.Handler, error) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, nil, fmt.Errorf("create Prometheus exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)), promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
}

// Handler builds the read-only diagnostics router.
func Handler(access Access, runtime Runtime) (http.Handler, error) {
	if runtime.Ara == nil || runtime.Sampler == nil || runtime.Logger == nil {
		return nil, errors.New("Ara client, sampler, and logger are required")
	}
	if err := access.Validate(); err != nil {
		return nil, err
	}
	if access.Listen == "" {
		return nil, errors.New("diagnostics listener address is required")
	}
	meter := runtime.Meter
	if meter == nil {
		meter = metricnoop.NewMeterProvider().Meter("github.com/cavenine/ara-mcp/internal/diagnostics")
	}
	signals, err := newDashboardMetrics(meter)
	if err != nil {
		return nil, err
	}
	health := &healthCache{}
	exportLimit := runtime.ExportLimit
	if exportLimit == 0 {
		exportLimit = 2
	}
	exports := make(chan struct{}, exportLimit)
	router := chi.NewRouter()
	router.Use(Middleware(runtime.Logger))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if access.Username != "" {
				username, password, ok := r.BasicAuth()
				validUser := subtle.ConstantTimeCompare([]byte(username), []byte(access.Username)) == 1
				validPassword := subtle.ConstantTimeCompare([]byte(password), []byte(access.Password)) == 1
				if !ok || !validUser || !validPassword {
					w.Header().Set("WWW-Authenticate", `Basic realm="ara-mcp diagnostics", charset="UTF-8"`)
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, map[string]string{"status": "ok"})
	})
	router.Get("/assets/datastar-0.21.4.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		_, _ = w.Write(datastarRuntime)
	})
	router.Get("/", func(w http.ResponseWriter, _ *http.Request) {
		araEvents := AraEventSnapshot{}
		if runtime.RecentAraEvents != nil {
			araEvents = runtime.RecentAraEvents()
		}
		fragment, err := dashboardFragment(runtime.Sampler.Snapshot(), false, araEvents, lastDashboardEvents(araEvents.Events))
		if err != nil {
			http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta name="color-scheme" content="dark"><title>OpenAstro Ara MCP</title><style>`+dashboardStyles+`</style><script type="module" src="/assets/datastar-0.21.4.js"></script></head><body class="starfield"><div id="stream" data-on-load="sse('/resources/stream')"></div>`+fragment+`<script>`+dashboardScript+`</script></body></html>`)
	})
	router.Get("/resources.csv", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		outcome := "success"
		source := exportSource(r)
		defer func() { recordExport(r, signals, "csv", source, outcome, started) }()
		if !acquire(exports) {
			outcome = "rejected"
			http.Error(w, "export limit reached", http.StatusServiceUnavailable)
			return
		}
		attrs := metric.WithAttributes(attribute.String("format", "csv"), attribute.String("source", source))
		signals.activeExports.Add(r.Context(), 1, attrs)
		defer signals.activeExports.Add(context.WithoutCancel(r.Context()), -1, attrs)
		defer func() { <-exports }()
		if r.URL.Query().Get("source") == "archive" {
			outcome = exportArchiveCSV(w, r, runtime.Archive)
			return
		}
		samples, ok := filteredSamples(w, r, runtime.Sampler.History())
		if !ok {
			outcome = "rejected"
			return
		}
		setExportRangeHeaders(w, samples)
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="ara-mcp-resources.csv"`)
		writer := csv.NewWriter(w)
		_ = writer.Write(resourceCSVHeader)
		for _, sample := range samples {
			if r.Context().Err() != nil {
				outcome = "cancelled"
				return
			}
			_ = writer.Write(resourceCSVRecord(sample))
		}
		writer.Flush()
		if writer.Error() != nil {
			outcome = "write_error"
		}
	})
	router.Get("/resources.jsonl", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		outcome := "success"
		source := exportSource(r)
		defer func() { recordExport(r, signals, "jsonl", source, outcome, started) }()
		if !acquire(exports) {
			outcome = "rejected"
			http.Error(w, "export limit reached", http.StatusServiceUnavailable)
			return
		}
		attrs := metric.WithAttributes(attribute.String("format", "jsonl"), attribute.String("source", source))
		signals.activeExports.Add(r.Context(), 1, attrs)
		defer signals.activeExports.Add(context.WithoutCancel(r.Context()), -1, attrs)
		defer func() { <-exports }()
		if r.URL.Query().Get("source") == "archive" {
			outcome = exportArchiveJSONL(w, r, runtime.Archive)
			return
		}
		samples, ok := filteredSamples(w, r, runtime.Sampler.History())
		if !ok {
			outcome = "rejected"
			return
		}
		setExportRangeHeaders(w, samples)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("Content-Disposition", `attachment; filename="ara-mcp-resources.jsonl"`)
		for _, sample := range samples {
			if r.Context().Err() != nil {
				outcome = "cancelled"
				return
			}
			if err := json.MarshalWrite(w, sample); err != nil {
				outcome = "write_error"
				return
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				outcome = "write_error"
				return
			}
		}
	})
	router.Get("/resources/stream", func(w http.ResponseWriter, r *http.Request) {
		updates, unsubscribe, ok := runtime.Sampler.Subscribe()
		if !ok {
			signals.subscriberRejects.Add(context.WithoutCancel(r.Context()), 1, metric.WithAttributes(attribute.String("reason", "limit")))
			http.Error(w, "dashboard subscriber limit reached", http.StatusServiceUnavailable)
			return
		}
		ctx := context.WithoutCancel(r.Context())
		signals.subscribers.Add(ctx, 1)
		streamOutcome := "cancelled"
		defer func() {
			signals.subscribers.Add(ctx, -1)
			signals.streams.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", streamOutcome)))
		}()
		defer unsubscribe()
		sse := datastar.NewSSE(w, r)
		lastInstance, lastSequence, cursorPresent, cursorValid := parseDashboardCursor(r.Header.Get("Last-Event-ID"))
		var araEventCursor dashboardAraEventCursor
		for {
			select {
			case sample, ok := <-updates:
				if !ok {
					streamOutcome = "closed"
					return
				}
				gap := cursorPresent && (!cursorValid || sample.InstanceID != lastInstance || sample.SampleSequence != lastSequence+1)
				if !cursorPresent && lastInstance != "" {
					gap = sample.InstanceID != lastInstance || sample.SampleSequence != lastSequence+1
				}
				araEvents := AraEventSnapshot{}
				if runtime.RecentAraEvents != nil {
					araEvents = runtime.RecentAraEvents()
				}
				newAraEvents := araEventCursor.advance(araEvents)
				fragment, err := dashboardFragment(sample, gap, araEvents, newAraEvents)
				id := sample.InstanceID + ":" + strconv.FormatUint(sample.SampleSequence, 10)
				if err != nil || sse.Send(datastar.EventType("datastar-merge-fragments"), []string{"selector #dashboard", "mergeMode morph", "fragments " + fragment}, datastar.WithSSEEventId(id)) != nil {
					streamOutcome = "write_error"
					return
				}
				lastInstance, lastSequence, cursorPresent, cursorValid = sample.InstanceID, sample.SampleSequence, true, true
			case <-r.Context().Done():
				return
			}
		}
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		reachable, _ := health.check(r.Context(), runtime.Ara, middleware.GetReqID(r.Context()))
		if !reachable {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, map[string]string{"status": "degraded", "reason": "ara_unavailable"})
			return
		}
		render.JSON(w, r, map[string]string{"status": "ready"})
	})
	router.Get("/status", func(w http.ResponseWriter, r *http.Request) {
		reachable, lastSuccessful := health.check(r.Context(), runtime.Ara, middleware.GetReqID(r.Context()))
		render.JSON(w, r, status{Version: runtime.Version, UptimeSeconds: time.Since(runtime.StartedAt).Seconds(), AraReachable: reachable, LastSuccessfulAraCheck: lastSuccessful, Process: runtime.Sampler.Snapshot()})
	})
	if runtime.Metrics != nil {
		router.Handle("/metrics", runtime.Metrics)
	}
	if access.Pprof {
		router.Get("/debug/pprof/", pprof.Index)
		router.Get("/debug/pprof/cmdline", pprof.Cmdline)
		router.Get("/debug/pprof/profile", pprof.Profile)
		router.Get("/debug/pprof/symbol", pprof.Symbol)
		router.Get("/debug/pprof/trace", pprof.Trace)
		for _, name := range []string{"allocs", "block", "goroutine", "heap", "mutex", "threadcreate"} {
			router.Handle("/debug/pprof/"+name, pprof.Handler(name))
		}
	}
	return router, nil
}

func acquire(semaphore chan struct{}) bool {
	select {
	case semaphore <- struct{}{}:
		return true
	default:
		return false
	}
}

func exportSource(r *http.Request) string {
	source := r.URL.Query().Get("source")
	if source == "" {
		return "live"
	}
	if source != "archive" {
		return "other"
	}
	return source
}

func recordExport(r *http.Request, signals dashboardMetrics, format, source, outcome string, started time.Time) {
	ctx := context.WithoutCancel(r.Context())
	attrs := metric.WithAttributes(attribute.String("format", format), attribute.String("source", source), attribute.String("outcome", outcome))
	signals.exports.Add(ctx, 1, attrs)
	signals.exportDuration.Record(ctx, time.Since(started).Seconds(), attrs)
}

var resourceCSVHeader = []string{"schema_version", "instance_id", "sample_sequence", "sampled_at_utc", "uptime_seconds", "cpu_percent_one_core", "logical_cpus", "rss_bytes", "go_alloc_bytes", "go_sys_bytes", "goroutines", "gomaxprocs", "heap_objects", "gc_count"}

func resourceCSVRecord(sample exportSample) []string {
	cpu, rss := "", ""
	if sample.CPUPercent != nil {
		cpu = strconv.FormatFloat(*sample.CPUPercent, 'f', -1, 64)
	}
	if sample.RSSBytes != nil {
		rss = strconv.FormatUint(*sample.RSSBytes, 10)
	}
	return []string{sample.SchemaVersion, sample.InstanceID, strconv.FormatUint(sample.SampleSequence, 10), sample.SampledAt.UTC().Format(time.RFC3339Nano), strconv.FormatFloat(sample.UptimeSeconds, 'f', -1, 64), cpu, strconv.Itoa(sample.LogicalCPUs), rss, strconv.FormatUint(sample.GoAllocBytes, 10), strconv.FormatUint(sample.GoSysBytes, 10), strconv.Itoa(sample.Goroutines), strconv.Itoa(sample.GOMaxProcs), strconv.FormatUint(sample.HeapObjects, 10), strconv.FormatUint(uint64(sample.GCCount), 10)}
}

type archiveExportStats struct {
	retainedCount int
	exportCount   int
	gapCount      int
	malformedRows int
	instanceID    string
	multiple      bool
	retainedStart time.Time
	retainedEnd   time.Time
	exportStart   time.Time
	exportEnd     time.Time
}

func scanArchive(ctx context.Context, segments []monitor.ArchiveSegment, query resourceQuery, write func(exportSample) error) (archiveExportStats, error) {
	var stats archiveExportStats
	var lastInstance string
	var lastSequence uint64
	haveLast := false
	for _, segment := range segments {
		scanner := bufio.NewScanner(io.NewSectionReader(segment.File, 0, segment.Size))
		scanner.Buffer(make([]byte, 4096), 64<<10)
		for scanner.Scan() {
			if err := ctx.Err(); err != nil {
				return stats, err
			}
			var sample monitor.Snapshot
			if err := json.Unmarshal(scanner.Bytes(), &sample); err != nil || sample.InstanceID == "" || sample.SampledAt.IsZero() {
				stats.gapCount++
				stats.malformedRows++
				continue
			}
			if query.InstanceID != "" && sample.InstanceID != query.InstanceID {
				continue
			}
			if sample.InstanceID != lastInstance {
				lastInstance, lastSequence, haveLast = sample.InstanceID, 0, false
			}
			if haveLast && sample.SampleSequence != lastSequence+1 {
				stats.gapCount++
			}
			lastSequence, haveLast = sample.SampleSequence, true
			stats.retainedCount++
			if stats.instanceID == "" {
				stats.instanceID = sample.InstanceID
			} else if stats.instanceID != sample.InstanceID {
				stats.multiple = true
			}
			if stats.retainedStart.IsZero() || sample.SampledAt.Before(stats.retainedStart) {
				stats.retainedStart = sample.SampledAt
			}
			if sample.SampledAt.After(stats.retainedEnd) {
				stats.retainedEnd = sample.SampledAt
			}
			if (!query.From.IsZero() && sample.SampledAt.Before(query.From)) || (!query.To.IsZero() && sample.SampledAt.After(query.To)) {
				continue
			}
			stats.exportCount++
			if stats.exportStart.IsZero() || sample.SampledAt.Before(stats.exportStart) {
				stats.exportStart = sample.SampledAt
			}
			if sample.SampledAt.After(stats.exportEnd) {
				stats.exportEnd = sample.SampledAt
			}
			if write != nil {
				if err := write(resourceSample(sample)); err != nil {
					return stats, err
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return stats, fmt.Errorf("read resource archive segment: %w", err)
		}
	}
	return stats, nil
}

func setArchiveRangeHeaders(w http.ResponseWriter, stats archiveExportStats) {
	w.Header().Set("X-Resource-Retained-Sample-Count", strconv.Itoa(stats.retainedCount))
	w.Header().Set("X-Resource-Export-Sample-Count", strconv.Itoa(stats.exportCount))
	w.Header().Set("X-Resource-Archive-Gap-Count", strconv.Itoa(stats.gapCount))
	w.Header().Set("X-Resource-Archive-Corrupt-Records", strconv.Itoa(stats.malformedRows))
	if stats.instanceID != "" {
		instance := stats.instanceID
		if stats.multiple {
			instance = "multiple"
		}
		w.Header().Set("X-Resource-Instance-ID", instance)
	}
	if !stats.retainedStart.IsZero() {
		w.Header().Set("X-Resource-Retained-Start", stats.retainedStart.UTC().Format(time.RFC3339Nano))
		w.Header().Set("X-Resource-Retained-End", stats.retainedEnd.UTC().Format(time.RFC3339Nano))
	}
	if !stats.exportStart.IsZero() {
		w.Header().Set("X-Resource-Export-Start", stats.exportStart.UTC().Format(time.RFC3339Nano))
		w.Header().Set("X-Resource-Export-End", stats.exportEnd.UTC().Format(time.RFC3339Nano))
	}
}

func exportArchiveCSV(w http.ResponseWriter, r *http.Request, archive *monitor.Archive) string {
	if archive == nil {
		http.Error(w, "archive source is disabled", http.StatusServiceUnavailable)
		return "rejected"
	}
	query, err := parseResourceQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "rejected"
	}
	segments, err := archive.OpenSnapshot()
	if err != nil {
		http.Error(w, "archive snapshot unavailable", http.StatusServiceUnavailable)
		return "write_error"
	}
	defer monitor.CloseArchiveSegments(segments)
	stats, err := scanArchive(r.Context(), segments, query, nil)
	if err != nil {
		if r.Context().Err() != nil {
			return "cancelled"
		}
		http.Error(w, "archive snapshot unavailable", http.StatusServiceUnavailable)
		return "write_error"
	}
	setArchiveRangeHeaders(w, stats)
	if (query.InstanceID != "" || !query.From.IsZero() || !query.To.IsZero()) && (stats.retainedCount == 0 || stats.exportCount == 0) {
		http.Error(w, "requested range is outside retained archive history", http.StatusRequestedRangeNotSatisfiable)
		return "rejected"
	}
	if !query.From.IsZero() && !stats.retainedStart.IsZero() && query.From.Before(stats.retainedStart) {
		w.Header().Set("X-Resource-Range-Truncated", "start")
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ara-mcp-resources-archive.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write(resourceCSVHeader)
	_, err = scanArchive(r.Context(), segments, query, func(sample exportSample) error { return writer.Write(resourceCSVRecord(sample)) })
	writer.Flush()
	if r.Context().Err() != nil {
		return "cancelled"
	}
	if err != nil || writer.Error() != nil {
		return "write_error"
	}
	return "success"
}

func exportArchiveJSONL(w http.ResponseWriter, r *http.Request, archive *monitor.Archive) string {
	if archive == nil {
		http.Error(w, "archive source is disabled", http.StatusServiceUnavailable)
		return "rejected"
	}
	query, err := parseResourceQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "rejected"
	}
	segments, err := archive.OpenSnapshot()
	if err != nil {
		http.Error(w, "archive snapshot unavailable", http.StatusServiceUnavailable)
		return "write_error"
	}
	defer monitor.CloseArchiveSegments(segments)
	stats, err := scanArchive(r.Context(), segments, query, nil)
	if err != nil {
		if r.Context().Err() != nil {
			return "cancelled"
		}
		http.Error(w, "archive snapshot unavailable", http.StatusServiceUnavailable)
		return "write_error"
	}
	setArchiveRangeHeaders(w, stats)
	if (query.InstanceID != "" || !query.From.IsZero() || !query.To.IsZero()) && (stats.retainedCount == 0 || stats.exportCount == 0) {
		http.Error(w, "requested range is outside retained archive history", http.StatusRequestedRangeNotSatisfiable)
		return "rejected"
	}
	if !query.From.IsZero() && !stats.retainedStart.IsZero() && query.From.Before(stats.retainedStart) {
		w.Header().Set("X-Resource-Range-Truncated", "start")
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="ara-mcp-resources-archive.jsonl"`)
	err = func() error {
		_, err := scanArchive(r.Context(), segments, query, func(sample exportSample) error {
			if err := json.MarshalWrite(w, sample); err != nil {
				return err
			}
			_, err := io.WriteString(w, "\n")
			return err
		})
		return err
	}()
	if err != nil {
		if r.Context().Err() != nil {
			return "cancelled"
		}
		return "write_error"
	}
	return "success"
}

type resourceQuery struct {
	Source     string
	InstanceID string
	From       time.Time
	To         time.Time
}

func parseResourceQuery(r *http.Request) (resourceQuery, error) {
	values := r.URL.Query()
	if len(values) > 4 {
		return resourceQuery{}, errors.New("unsupported query parameter")
	}
	for key, items := range values {
		if (key != "from" && key != "to" && key != "instance_id" && key != "source") || len(items) != 1 {
			return resourceQuery{}, errors.New("unsupported or repeated query parameter")
		}
	}
	query := resourceQuery{Source: values.Get("source"), InstanceID: values.Get("instance_id")}
	if query.Source == "" {
		query.Source = "live"
	}
	if query.Source != "live" && query.Source != "archive" {
		return resourceQuery{}, errors.New("source must be live or archive")
	}
	for key, target := range map[string]*time.Time{"from": &query.From, "to": &query.To} {
		if value := values.Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return resourceQuery{}, errors.New("time filters must be RFC3339 timestamps")
			}
			*target = parsed
		}
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.From.After(query.To) {
		return resourceQuery{}, errors.New("from must not be after to")
	}
	return query, nil
}

func filteredSamples(w http.ResponseWriter, r *http.Request, samples []monitor.Snapshot) ([]exportSample, bool) {
	setRetainedRangeHeaders(w, samples)
	query, err := parseResourceQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	if query.Source != "live" {
		http.Error(w, "archive source is not available", http.StatusServiceUnavailable)
		return nil, false
	}
	if query.InstanceID != "" && len(samples) > 0 && samples[0].InstanceID != query.InstanceID {
		http.Error(w, "instance_id is not retained", http.StatusRequestedRangeNotSatisfiable)
		return nil, false
	}
	if len(samples) > 0 && ((!query.To.IsZero() && query.To.Before(samples[0].SampledAt)) || (!query.From.IsZero() && query.From.After(samples[len(samples)-1].SampledAt))) {
		http.Error(w, "requested time range is outside retained history", http.StatusRequestedRangeNotSatisfiable)
		return nil, false
	}
	if len(samples) > 0 && !query.From.IsZero() && query.From.Before(samples[0].SampledAt) {
		w.Header().Set("X-Resource-Range-Truncated", "start")
	}
	result := make([]exportSample, 0, len(samples))
	for _, sample := range samples {
		if (query.InstanceID == "" || sample.InstanceID == query.InstanceID) && (query.From.IsZero() || !sample.SampledAt.Before(query.From)) && (query.To.IsZero() || !sample.SampledAt.After(query.To)) {
			result = append(result, resourceSample(sample))
		}
	}
	return result, true
}

func setRetainedRangeHeaders(w http.ResponseWriter, samples []monitor.Snapshot) {
	w.Header().Set("X-Resource-Retained-Sample-Count", strconv.Itoa(len(samples)))
	if len(samples) == 0 {
		return
	}
	w.Header().Set("X-Resource-Instance-ID", samples[0].InstanceID)
	w.Header().Set("X-Resource-Retained-Start", samples[0].SampledAt.UTC().Format(time.RFC3339Nano))
	w.Header().Set("X-Resource-Retained-End", samples[len(samples)-1].SampledAt.UTC().Format(time.RFC3339Nano))
}

func setExportRangeHeaders(w http.ResponseWriter, samples []exportSample) {
	w.Header().Set("X-Resource-Export-Sample-Count", strconv.Itoa(len(samples)))
	if len(samples) == 0 {
		return
	}
	w.Header().Set("X-Resource-Export-Start", samples[0].SampledAt.UTC().Format(time.RFC3339Nano))
	w.Header().Set("X-Resource-Export-End", samples[len(samples)-1].SampledAt.UTC().Format(time.RFC3339Nano))
}

// Middleware adds a trusted request ID, structured request logging, and panic recovery.
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return MiddlewareFor(logger, "diagnostics_http")
}

// MiddlewareFor applies the shared request-ID, structured-log, and panic-recovery stack.
func MiddlewareFor(logger *slog.Logger, component string) func(http.Handler) http.Handler {
	return MiddlewareForFaults(logger, component, nil)
}

// MiddlewareForFaults applies the shared stack and reports recovered panics to onFault.
func MiddlewareForFaults(logger *slog.Logger, component string, onFault func(*http.Request)) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return discardRequestID(middleware.RequestID(middleware.RequestLogger(logFormatter{logger: logger, component: component, onFault: onFault})(middleware.Recoverer(next))))
	}
}

type status struct {
	Version                string           `json:"version"`
	UptimeSeconds          float64          `json:"uptime_seconds"`
	AraReachable           bool             `json:"ara_reachable"`
	LastSuccessfulAraCheck *time.Time       `json:"last_successful_ara_check"`
	Process                monitor.Snapshot `json:"process"`
}

type healthCache struct {
	mu        sync.Mutex
	lastCheck time.Time
	lastGood  time.Time
	reachable bool
	checking  bool
	wait      chan struct{}
}

func (h *healthCache) check(ctx context.Context, client *ara.Client, requestID string) (bool, *time.Time) {
	for {
		h.mu.Lock()
		now := time.Now()
		if !h.lastCheck.IsZero() && now.Sub(h.lastCheck) < 5*time.Second {
			reachable, lastGood := h.snapshotLocked()
			h.mu.Unlock()
			return reachable, lastGood
		}
		if h.checking {
			wait := h.wait
			h.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return false, nil
			}
		}
		h.checking = true
		h.wait = make(chan struct{})
		wait := h.wait
		h.mu.Unlock()

		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := client.CheckServerWithRequestID(checkCtx, requestID)
		cancel()
		h.mu.Lock()
		if ctx.Err() == nil {
			h.lastCheck = time.Now()
			h.reachable = err == nil
			if err == nil {
				h.lastGood = h.lastCheck.UTC()
			}
		}
		h.checking = false
		close(wait)
		reachable, lastGood := h.snapshotLocked()
		h.mu.Unlock()
		return reachable, lastGood
	}
}

func (h *healthCache) snapshotLocked() (bool, *time.Time) {
	if h.lastGood.IsZero() {
		return h.reachable, nil
	}
	last := h.lastGood
	return h.reachable, &last
}

func discardRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(middleware.RequestIDHeader)
		next.ServeHTTP(w, r)
	})
}

type logFormatter struct {
	logger    *slog.Logger
	component string
	onFault   func(*http.Request)
}
type logEntry struct {
	logger    *slog.Logger
	component string
	onFault   func(*http.Request)
	request   *http.Request
	requestID string
	method    string
	failed    bool
}

func (f logFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return &logEntry{logger: f.logger, component: f.component, onFault: f.onFault, request: r, requestID: middleware.GetReqID(r.Context()), method: r.Method}
}
func (e *logEntry) Write(status, bytes int, _ http.Header, elapsed time.Duration, _ any) {
	route := chi.RouteContext(e.request.Context()).RoutePattern()
	if route == "" {
		route = "unmatched"
	}
	if status < http.StatusBadRequest && (route == "/healthz" || route == "/readyz" || route == "/metrics") {
		return
	}
	level := slog.LevelInfo
	event := "http_request"
	message := "http request completed"
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	} else if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	if e.failed {
		level, event, message = slog.LevelError, "http_fault", "http request failed"
	}
	e.logger.Log(e.request.Context(), level, message, "service", "ara-mcp", "component", e.component, "event", event, "http_request_id", e.requestID, "method", e.method, "route", route, "http_status", status, "response_bytes", bytes, "duration_seconds", elapsed.Seconds())
}
func (e *logEntry) Panic(_ any, _ []byte) {
	e.failed = true
	if e.onFault != nil {
		e.onFault(e.request)
	}
}
