// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package diagnostics

import (
	"bufio"
	"bytes"
	"encoding/csv"
	json "encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestHandlerProbesAccessAndStatus(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/info" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0", Username: "operator", Password: "secret"}, Runtime{
		Ara: client, Sampler: monitor.NewSampler(), Logger: logger, Version: "test", StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		user string
		pass string
		want int
	}{
		{name: "unauthorized", path: "/healthz", want: http.StatusUnauthorized},
		{name: "unauthorized dashboard", path: "/", want: http.StatusUnauthorized},
		{name: "unauthorized resource export", path: "/resources.csv", want: http.StatusUnauthorized},
		{name: "health", path: "/healthz", user: "operator", pass: "secret", want: http.StatusOK},
		{name: "ready", path: "/readyz", user: "operator", pass: "secret", want: http.StatusOK},
		{name: "status", path: "/status", user: "operator", pass: "secret", want: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			req.Header.Set("X-Request-ID", "caller-controlled")
			if test.user != "" {
				req.SetBasicAuth(test.user, test.pass)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if response.Code == http.StatusOK && test.path != "/" && test.path != "/resources.csv" && !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
			}
			if response.Header().Get("X-Request-ID") == "caller-controlled" {
				t.Fatal("trusted caller-supplied request ID")
			}
		})
	}
	if got := logs.String(); !strings.Contains(got, `"http_status":401`) || !strings.Contains(got, `"route":"unmatched"`) || strings.Contains(got, "caller-controlled") {
		t.Fatalf("request logs missing safe access record: %s", got)
	}
}

func TestHandlerPprofIsOptInAndRequiresBasicAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	runtime := Runtime{Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()}
	if _, err := Handler(Access{Username: "operator", Password: "secret", Pprof: true}, runtime); err == nil {
		t.Fatal("pprof without a diagnostics listener was accepted")
	}
	if _, err := Handler(Access{Listen: "127.0.0.1:0", Pprof: true}, runtime); err == nil {
		t.Fatal("pprof was enabled without Basic-auth credentials")
	}
	protected, err := Handler(Access{Listen: "127.0.0.1:0", Username: "operator", Password: "secret", Pprof: true}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		path string
		user string
		want int
	}{
		{name: "index unauthorized", path: "/debug/pprof/", want: http.StatusUnauthorized},
		{name: "index authorized", path: "/debug/pprof/", user: "operator", want: http.StatusOK},
		{name: "goroutine profile authorized", path: "/debug/pprof/goroutine?debug=1", user: "operator", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.user != "" {
				req.SetBasicAuth(test.user, "secret")
			}
			response := httptest.NewRecorder()
			protected.ServeHTTP(response, req)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
		})
	}
	disabled, err := Handler(Access{Listen: "127.0.0.1:0"}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	disabled.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("disabled pprof status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestHandlerReadinessDegradesWithoutAra(t *testing.T) {
	client, err := ara.New(ara.Config{BaseURL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != want {
			t.Errorf("%s status = %d, want %d", path, response.Code, want)
		}
	}
}

func TestHandlerSharesRecentAraHealthChecks(t *testing.T) {
	var requests atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/readyz", "/status", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Ara health requests = %d, want cached single request", got)
	}
}

func TestMiddlewarePreservesFlushingAndRecovers(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	flushed := false
	handler := Middleware(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("middleware removed http.Flusher")
			return
		}
		flusher.Flush()
		flushed = true
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/stream", nil))
	if !flushed {
		t.Fatal("wrapped response was not flushed")
	}

	panicHandler := Middleware(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("sensitive panic detail") }))
	response = httptest.NewRecorder()
	panicHandler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/fault", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", response.Code)
	}
	output := logs.String()
	if !strings.Contains(output, `"event":"http_fault"`) || strings.Contains(output, "sensitive panic detail") {
		t.Fatalf("fault log not sanitized/structured: %s", output)
	}
}

func TestNewMetricsExportsOpenTelemetryInPrometheusFormat(t *testing.T) {
	provider, handler, err := NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	})
	counter, err := provider.Meter("test").Int64Counter("diagnostics.test.calls")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(t.Context(), 1, metric.WithAttributes(attribute.String("outcome", "success")))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "diagnostics_test_calls_total") {
		t.Fatalf("metrics response = %d %q", response.Code, response.Body.String())
	}
}

func TestResourceExportsUseSharedSampleHistory(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	sampler := monitor.NewSampler()
	sample := sampler.Snapshot()
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: sampler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, contentType, want string
	}{{"/resources.csv", "text/csv", "sample_sequence"}, {"/resources.jsonl", "application/x-ndjson", sample.InstanceID}} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("resource export = %d %q %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			if response.Header().Get("X-Resource-Instance-ID") != sample.InstanceID || response.Header().Get("X-Resource-Retained-Sample-Count") != "1" || response.Header().Get("X-Resource-Export-Sample-Count") != "1" || response.Header().Get("X-Resource-Export-Start") == "" {
				t.Fatalf("resource export range headers = %v", response.Header())
			}
			if test.path == "/resources.csv" {
				rows, err := csv.NewReader(strings.NewReader(response.Body.String())).ReadAll()
				if err != nil || len(rows) != 2 || rows[1][2] != "1" || rows[1][1] != sample.InstanceID {
					t.Fatalf("CSV snapshot = %v, %v", rows, err)
				}
			} else {
				var exported exportSample
				if err := json.Unmarshal([]byte(response.Body.String()), &exported); err != nil || exported.SampleSequence != sample.SampleSequence || exported.InstanceID != sample.InstanceID {
					t.Fatalf("JSONL snapshot = %+v, %v", exported, err)
				}
			}
		})
	}
}

func TestHandlerServesPinnedDatastarRuntimeLocally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/assets/datastar-0.21.4.js", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "javascript") || !strings.Contains(response.Body.String(), "Datastar v0.21.4") {
		t.Fatalf("pinned Datastar runtime = status %d type %q body starts %q", response.Code, response.Header().Get("Content-Type"), response.Body.String()[:min(len(response.Body.String()), 80)])
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `src="/assets/datastar-0.21.4.js"`) || strings.Contains(response.Body.String(), "cdn.") {
		t.Fatalf("dashboard page does not use its local Datastar runtime: %s", response.Body.String())
	}
	for _, feature := range []string{
		`class="starfield"`,
		`aria-label="CPU usage gauge"`,
		`aria-label="RSS memory usage gauge"`,
		`aria-label="CPU usage over time"`,
		`aria-label="Memory usage over time"`,
		`aria-label="Ara server events"`,
		`id="ara-event-rows"`,
		`if(event.exposure){`,
		`guide error `,
		`if(event.autofocus){`,
		`final HFR `,
		`marker Δ `,
		`collimation `,
		`Newest first`,
		`prefers-reduced-motion:reduce`,
	} {
		if !strings.Contains(response.Body.String(), feature) {
			t.Errorf("dashboard missing %q", feature)
		}
	}
}

func TestResourceStreamSendsCurrentSampleAndFlushes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	sampler := monitor.NewSampler()
	sample := sampler.Snapshot()
	thirtySeconds, elapsedMS, guideFrame, raArcsec, focusStep, focusHFR := 30.0, int64(30120), 42, 1.5, 4, 2.35
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{
		Ara: client, Sampler: sampler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now(),
		RecentAraEvents: func() AraEventSnapshot {
			return AraEventSnapshot{Available: true, LastSequence: 10, Events: []ara.WebSocketEvent{
				{Type: "equipment.connected", Timestamp: "2026-10-06T16:00:00Z", Seq: 7, DeviceType: "camera", DeviceID: "camera-1", DeviceName: "ASI2600", State: "connected"},
				{Type: "camera.exposure_complete", Seq: 8, Exposure: &ara.ExposureEventContext{FrameID: "frame-1", ExposureSec: &thirtySeconds, ElapsedMS: &elapsedMS}},
				{Type: "guider.step", Seq: 9, Guider: &ara.GuiderEventContext{Frame: &guideFrame, RAArcsec: &raArcsec}},
				{Type: "autofocus.step_complete", Seq: 10, Autofocus: &ara.AutofocusEventContext{StepIndex: &focusStep, HFR: &focusHFR}},
			}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Get(server.URL + "/resources/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
		event.WriteString(line)
	}
	if !strings.Contains(event.String(), "event: datastar-merge-fragments\n") || !strings.Contains(event.String(), "id: "+sample.InstanceID+":"+strconv.FormatUint(sample.SampleSequence, 10)+"\n") || !strings.Contains(event.String(), "data: selector #dashboard\n") || !strings.Contains(event.String(), "data: fragments <main id=\"dashboard\" class=\"page-shell\">") || !strings.Contains(event.String(), sample.InstanceID) || !strings.Contains(event.String(), "equipment.connected") || !strings.Contains(event.String(), "ASI2600") || !strings.Contains(event.String(), "camera-1") || !strings.Contains(event.String(), "2026-10-06T16:00:00Z") || !strings.Contains(event.String(), "camera.exposure_complete") || !strings.Contains(event.String(), "exposure_sec") || !strings.Contains(event.String(), "ra_arcsec") || !strings.Contains(event.String(), "autofocus.step_complete") || !strings.Contains(event.String(), "step_index") {
		t.Fatalf("Datastar patch event = %q", event.String())
	}
}

func TestDashboardAraEventCursorSendsInitialAndNewEvents(t *testing.T) {
	var cursor dashboardAraEventCursor
	initial := AraEventSnapshot{Available: true, LastSequence: 11, Events: []ara.WebSocketEvent{{Type: "sequence.started", Seq: 10}, {Type: "sequence.progress", Seq: 11}}}
	if events := cursor.advance(initial); len(events) != 2 || events[0].Seq != 10 || events[1].Seq != 11 {
		t.Fatalf("initial events = %+v", events)
	}
	incremental := AraEventSnapshot{Available: true, LastSequence: 12, Events: []ara.WebSocketEvent{{Type: "sequence.started", Seq: 10}, {Type: "sequence.progress", Seq: 11}, {Type: "sequence.complete", Seq: 12}}}
	if events := cursor.advance(incremental); len(events) != 1 || events[0].Seq != 12 {
		t.Fatalf("incremental events = %+v; want only sequence 12", events)
	}
	cursor.advance(AraEventSnapshot{LastSequence: 12})
	newSession := AraEventSnapshot{Available: true, LastSequence: 2, Events: []ara.WebSocketEvent{{Type: "sequence.started", Seq: 2}}}
	if events := cursor.advance(newSession); len(events) != 1 || events[0].Seq != 2 {
		t.Fatalf("new session events = %+v; want sequence 2 after session reset", events)
	}
	buffer := make([]ara.WebSocketEvent, dashboardEventLimit+2)
	for i := range buffer {
		buffer[i].Seq = int64(i + 1)
	}
	var boundedCursor dashboardAraEventCursor
	bounded := boundedCursor.advance(AraEventSnapshot{Available: true, LastSequence: int64(len(buffer)), Events: buffer})
	if len(bounded) != dashboardEventLimit {
		t.Fatalf("initial dashboard event window = %d entries; want %d", len(bounded), dashboardEventLimit)
	}
	if bounded[0].Seq != 3 || bounded[len(bounded)-1].Seq != int64(len(buffer)) {
		t.Fatalf("initial dashboard event window = %d entries from %d to %d", len(bounded), bounded[0].Seq, bounded[len(bounded)-1].Seq)
	}
}

func TestResourceStreamMarksReplayedGap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	sampler := monitor.NewSampler()
	sample := sampler.Snapshot()
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: sampler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/resources/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Last-Event-ID", sample.InstanceID+":"+strconv.FormatUint(sample.SampleSequence+100, 10))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\n" {
			break
		}
		event.WriteString(line)
	}
	if !strings.Contains(event.String(), "id: "+sample.InstanceID+":"+strconv.FormatUint(sample.SampleSequence, 10)+"\n") || !strings.Contains(event.String(), "history gap or process restart was detected") {
		t.Fatalf("replay gap response = %q", event.String())
	}
}

func TestResourceExportRejectsInvalidRangeBeforeStreaming(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.csv?from=not-a-time", nil))
	if response.Code != http.StatusBadRequest || strings.Contains(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("invalid range response = %d, headers %v", response.Code, response.Header())
	}
}

func TestResourceExportReportsAndRejectsUnavailableRetainedRange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	sampler := monitor.NewSampler()
	sample := sampler.Snapshot()
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{Ara: client, Sampler: sampler, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{
		"from": {sample.SampledAt.Add(-2 * time.Hour).Format(time.RFC3339Nano)},
		"to":   {sample.SampledAt.Add(-time.Hour).Format(time.RFC3339Nano)},
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.csv?"+values.Encode(), nil))
	if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Header().Get("X-Resource-Retained-Start") == "" || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("unavailable range = status %d headers %v body %q", response.Code, response.Header(), response.Body.String())
	}
	values.Del("to")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.jsonl?"+values.Encode(), nil))
	if response.Code != http.StatusOK || response.Header().Get("X-Resource-Range-Truncated") != "start" || response.Header().Get("X-Resource-Export-Sample-Count") != "1" {
		t.Fatalf("partially retained range = status %d headers %v body %q", response.Code, response.Header(), response.Body.String())
	}
}

func TestArchiveExportsStreamRetainedJSONLAndCSV(t *testing.T) {
	directory := t.TempDir()
	archive, err := monitor.OpenArchive(directory, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	for _, sequence := range []uint64{1, 2, 4} {
		if !archive.Record(monitor.Snapshot{SchemaVersion: "1", InstanceID: "archived-instance", SampleSequence: sequence, SampledAt: time.Unix(int64(sequence), 0), Goroutines: int(sequence)}) {
			t.Fatalf("archive rejected sample %d", sequence)
		}
	}
	if err := archive.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	segments, err := archive.OpenSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	segmentPath := filepath.Join(directory, segments[0].Name)
	monitor.CloseArchiveSegments(segments)
	segmentFile, err := os.OpenFile(segmentPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(segmentFile, "{broken archive row}\n"); err != nil {
		segmentFile.Close()
		t.Fatal(err)
	}
	if err := segmentFile.Close(); err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{
		Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		StartedAt: time.Now(), Archive: archive,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, contentType string
	}{{"/resources.jsonl?source=archive&instance_id=archived-instance", "application/x-ndjson"}, {"/resources.csv?source=archive&instance_id=archived-instance", "text/csv"}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), test.contentType) || response.Header().Get("X-Resource-Export-Sample-Count") != "3" || response.Header().Get("X-Resource-Archive-Gap-Count") != "2" || response.Header().Get("X-Resource-Archive-Corrupt-Records") != "1" || !strings.Contains(response.Body.String(), "archived-instance") {
			t.Fatalf("archive export %s = status %d headers %v body %q", test.path, response.Code, response.Header(), response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.jsonl?source=archive&instance_id=missing", nil))
	if response.Code != http.StatusRequestedRangeNotSatisfiable || response.Header().Get("Content-Disposition") != "" {
		t.Fatalf("missing archive instance = status %d headers %v", response.Code, response.Header())
	}
}

func TestResourceExportsRecordMetricsAndBalanceActiveCount(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(t.Context()); err != nil {
			t.Error(err)
		}
	})
	handler, err := Handler(Access{Listen: "127.0.0.1:0"}, Runtime{
		Ara: client, Sampler: monitor.NewSampler(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		StartedAt: time.Now(), Meter: provider.Meter("dashboard-test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.csv", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("CSV export status = %d", response.Code)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/resources.jsonl?from=invalid", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid export status = %d, want 400", response.Code)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var exports, active int64
	var durationCount uint64
	for _, scope := range metrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			switch item.Name {
			case "resource.dashboard.exports", "resource.dashboard.active_exports":
				sum, ok := item.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric %s data = %T", item.Name, item.Data)
				}
				for _, point := range sum.DataPoints {
					if item.Name == "resource.dashboard.exports" {
						exports += point.Value
					} else {
						active += point.Value
					}
				}
			case "resource.dashboard.export.duration":
				histogram, ok := item.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("metric %s data = %T", item.Name, item.Data)
				}
				for _, point := range histogram.DataPoints {
					durationCount += point.Count
				}
			}
		}
	}
	if exports != 2 || active != 0 || durationCount != 2 {
		t.Fatalf("exports=%d active=%d duration observations=%d; want 2, 0, 2", exports, active, durationCount)
	}
}
