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

// Runtime holds dependencies shared with the stdio server.
type Runtime struct {
	Ara         *ara.Client
	Sampler     *monitor.Sampler
	Logger      *slog.Logger
	Version     string
	StartedAt   time.Time
	Metrics     http.Handler
	Meter       metric.Meter
	ExportLimit int
	Archive     *monitor.Archive
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

func dashboardFragment(sample monitor.Snapshot, gap bool) (string, error) {
	data, err := json.Marshal(resourceSample(sample))
	if err != nil {
		return "", fmt.Errorf("marshal dashboard sample: %w", err)
	}
	sampledAt := sample.SampledAt.UTC().Format(time.RFC3339Nano)
	status := "Last sample: " + sampledAt
	if gap {
		status = "A history gap or process restart was detected; showing latest sample: " + sampledAt
	}
	return `<main id="dashboard"><h1>ara-mcp resources</h1><p id="freshness" data-sampled-at="` + html.EscapeString(sampledAt) + `" aria-live="polite">` + html.EscapeString(status) + ` · CPU % uses one logical core · RSS/Go memory in bytes</p><pre id="sample">` + html.EscapeString(string(data)) + `</pre><p><a href="/resources.csv">Download CSV</a> · <a href="/resources.jsonl">Download JSONL</a></p></main>`, nil
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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>ara-mcp resources</title><script type="module" src="/assets/datastar-0.21.4.js"></script><div id="stream" data-on-load="sse('/resources/stream')"></div><main id="dashboard"><h1>ara-mcp resources</h1><p id="freshness" aria-live="polite">Waiting for sample…</p><pre id="sample">No sample yet</pre><p><a href="/resources.csv">Download CSV</a> · <a href="/resources.jsonl">Download JSONL</a></p></main><script>setInterval(()=>{const node=document.querySelector('#freshness');const sampled=Date.parse(node?.dataset.sampledAt||'');if(Number.isFinite(sampled)&&Date.now()-sampled>5000)node.textContent='Stale: no sample received for 5 seconds'},1000)</script></html>`)
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
				fragment, err := dashboardFragment(sample, gap)
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
