// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package diagnostics

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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
			if response.Code == http.StatusOK && !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
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
