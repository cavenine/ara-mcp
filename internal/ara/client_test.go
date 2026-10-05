// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDo_TimeoutLeavesMutationUnknown(t *testing.T) {
	var requests atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-release
	}))
	defer server.Close()
	defer close(release)

	client, err := New(Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.Do(t.Context(), Request{
		Method:     http.MethodPost,
		Route:      "/sequences/{id}/start",
		PathParams: map[string]string{"id": "7db1118d-57d9-4657-9779-8e1e619b12c4"},
	}, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want timeout")
	}
	if result.Outcome != OutcomeUnknown {
		t.Fatalf("Do() outcome = %q, want %q", result.Outcome, OutcomeUnknown)
	}
	if result.RetrySafe {
		t.Fatal("start timeout marked retry-safe without Ara idempotency support")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want context deadline exceeded", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestNew_RejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "relative base url", config: Config{BaseURL: "/api/v1"}},
		{name: "url credentials", config: Config{BaseURL: "http://user:password@example.test"}},
		{name: "unsupported scheme", config: Config{BaseURL: "ftp://example.test"}},
		{name: "negative timeout", config: Config{BaseURL: "http://example.test", Timeout: -time.Second}},
		{name: "oversized response limit", config: Config{BaseURL: "http://example.test", MaxResponseBytes: defaultMaxResponseBytes + 1}},
		{name: "too many read retries", config: Config{BaseURL: "http://example.test", ReadRetries: maxReadRetries + 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("New() error = nil, want invalid configuration error")
			}
		})
	}
}

func TestDo_RejectsInvalidRouteBeforeDispatch(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Do(t.Context(), Request{
		Method:     http.MethodGet,
		Route:      "/sequences/{id}",
		PathParams: map[string]string{"id": "one/two"},
	}, nil)
	if err == nil {
		t.Fatal("Do() error = nil for a path parameter containing a slash")
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("upstream requests = %d, want 0", got)
	}
}

func TestDo_IdempotentSequenceCreateIsTheOnlyRetrySafeUnknown(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	}))
	defer server.Close()
	defer close(release)
	client, err := New(Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Do(t.Context(), Request{
		Method:         http.MethodPost,
		Route:          "/sequences",
		Body:           []byte(`{"name":"plan","body":{}}`),
		IdempotencyKey: "create-replay-01",
	}, nil)
	if err == nil {
		t.Fatal("Do() error = nil, want timeout")
	}
	if result.Outcome != OutcomeUnknown || !result.RetrySafe {
		t.Fatalf("result = %+v, want unknown and retry-safe for keyed sequence create", result)
	}
}

func TestDo_PreservesRequestAndDecodesResponse(t *testing.T) {
	body := []byte("{ \"name\": \"M31\", \"body\": {\"$type\":\"TakeExposure\"} }")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sequences" {
			t.Errorf("request = %s %s, want POST /api/v1/sequences", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("name"); got != "M31 test" {
			t.Errorf("query name = %q, want %q", got, "M31 test")
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "intent-01" {
			t.Errorf("idempotency key = %q, want intent-01", got)
		}
		if got := r.Header.Get("X-Request-ID"); got != "local-request-01" {
			t.Errorf("request id = %q, want local-request-01", got)
		}
		got, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if string(got) != string(body) {
			t.Errorf("request body = %q, want exact opaque bytes %q", got, body)
		}
		w.Header().Set("X-Request-ID", "ara-request-01")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"7db1118d-57d9-4657-9779-8e1e619b12c4"}`)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL + "/api/v1"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID string `json:"id"`
	}
	result, err := client.Do(t.Context(), Request{
		Method:         http.MethodPost,
		Route:          "/sequences",
		Query:          map[string][]string{"name": {"M31 test"}},
		Body:           body,
		IdempotencyKey: "intent-01",
		RequestID:      "local-request-01",
	}, &decoded)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted || result.Status != http.StatusCreated {
		t.Fatalf("result = %+v, want completed HTTP 201", result)
	}
	if result.RequestID != "ara-request-01" {
		t.Errorf("response request id = %q, want ara-request-01", result.RequestID)
	}
	if decoded.ID != "7db1118d-57d9-4657-9779-8e1e619b12c4" {
		t.Errorf("decoded id = %q", decoded.ID)
	}
}

func TestDo_EmptyAcceptedBodyAndSanitizedProblem(t *testing.T) {
	t.Run("empty accepted response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodPost, Route: "/server/emergency-stop"}, new(struct{}))
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != OutcomeAccepted {
			t.Fatalf("outcome = %q, want %q", result.Outcome, OutcomeAccepted)
		}
	})

	t.Run("receipt remains acceptance", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"receipt-01"}`)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodPost, Route: "/sequences/{id}/start", PathParams: map[string]string{"id": "run-sequence"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != OutcomeAccepted || result.ReceiptID != "receipt-01" {
			t.Fatalf("result = %+v, want accepted receipt", result)
		}
	})

	t.Run("problem response redacts credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"type":"about:blank","title":"conflict","detail":"Authorization: Bearer abc123, standalone Bearer exposed123, see https://user:pass@example.test/?token=private"}`)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodPost, Route: "/server/connect"}, nil)
		if result.Outcome != OutcomeFailed {
			t.Errorf("outcome = %q, want %q", result.Outcome, OutcomeFailed)
		}
		var apiError *APIError
		if !errors.As(err, &apiError) {
			t.Fatalf("error = %v, want *APIError", err)
		}
		if strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), "exposed123") || strings.Contains(err.Error(), "user:pass") || strings.Contains(err.Error(), "private") {
			t.Errorf("error exposes credentials: %v", err)
		}
	})
}

func TestDo_ReadRetriesAndMutationIsNotRetried(t *testing.T) {
	t.Run("transient read retry", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if requests.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, ReadRetries: 1})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			OK bool `json:"ok"`
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodGet, Route: "/server/info"}, &body)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != OutcomeCompleted || !body.OK || requests.Load() != 2 {
			t.Fatalf("result=%+v body=%+v requests=%d", result, body, requests.Load())
		}
	})

	t.Run("transient mutation remains unknown", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, ReadRetries: 2})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{
			Method:     http.MethodPost,
			Route:      "/sequences/{id}/start",
			PathParams: map[string]string{"id": "7db1118d-57d9-4657-9779-8e1e619b12c4"},
		}, nil)
		if err == nil || result.Outcome != OutcomeUnknown || result.RetrySafe || requests.Load() != 1 {
			t.Fatalf("result=%+v error=%v requests=%d", result, err, requests.Load())
		}
	})

	t.Run("mutation redirect is not followed", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.URL.Path == "/api/v1/target" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Redirect(w, r, "/api/v1/target", http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodPost, Route: "/server/connect"}, nil)
		if err == nil || result.Outcome != OutcomeUnknown || requests.Load() != 1 {
			t.Fatalf("result=%+v error=%v requests=%d, want one uncertain non-followed request", result, err, requests.Load())
		}
	})
}

func TestDo_CursorPageAndBoundedResponse(t *testing.T) {
	t.Run("preserves opaque cursor", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("cursor"); got != "opaque+cursor/with?reserved" {
				t.Errorf("cursor = %q", got)
			}
			if got := r.URL.Query().Get("limit"); got != "25" {
				t.Errorf("limit = %q, want 25", got)
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"one"}],"next_cursor":"next","has_more":true}`)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		var page Page[struct {
			ID string `json:"id"`
		}]
		result, err := client.Do(t.Context(), Request{
			Method: http.MethodGet,
			Route:  "/sequences",
			Query:  map[string][]string{"limit": {"25"}, "cursor": {"opaque+cursor/with?reserved"}},
		}, &page)
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != OutcomeCompleted || !page.HasMore || page.NextCursor == nil || *page.NextCursor != "next" || len(page.Items) != 1 || page.Items[0].ID != "one" {
			t.Fatalf("result=%+v page=%+v", result, page)
		}
	})

	t.Run("rejects oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"long":"response"}`)
		}))
		defer server.Close()
		client, err := New(Config{BaseURL: server.URL, MaxResponseBytes: 8})
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Do(t.Context(), Request{Method: http.MethodGet, Route: "/server/info"}, new(struct{}))
		var requestError *RequestError
		if !errors.As(err, &requestError) || requestError.Class != "response_too_large" || result.Outcome != OutcomeFailed {
			t.Fatalf("result=%+v error=%v, want bounded response error", result, err)
		}
	})
}

func TestListSequences_UsesBoundedCurrentAraContract(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("limit"); got != "50" {
			t.Errorf("limit = %q, want default 50", got)
		}
		if got := r.URL.Query().Get("cursor"); got != "" {
			t.Errorf("cursor = %q, current Ara contract does not support cursor continuation", got)
		}
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	page, result, err := client.ListSequences(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted || page.HasMore || page.NextCursor != nil {
		t.Fatalf("result=%+v page=%+v", result, page)
	}
	if _, _, err := client.ListSequences(t.Context(), maxSequenceLimit+1); err == nil {
		t.Fatal("ListSequences() error = nil for an out-of-range limit")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
}

func TestListSequencesWithRequestIDPropagatesCorrelation(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Request-ID")
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ListSequencesWithRequestID(t.Context(), 7, "tool-request-123"); err != nil {
		t.Fatal(err)
	}
	if got != "tool-request-123" {
		t.Fatalf("Ara request ID = %q, want tool-request-123", got)
	}
}

func TestDo_RecordsSanitizedMetricsAndTraceForDecodeFailure(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer func() {
		if err := meterProvider.Shutdown(t.Context()); err != nil {
			t.Errorf("shutdown meter provider: %v", err)
		}
	}()
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	defer func() {
		if err := tracerProvider.Shutdown(t.Context()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	}()

	var traceparentCount atomic.Int64
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Traceparent") != "" {
			traceparentCount.Add(1)
		}
		if r.URL.Path == "/api/v1/server/state" {
			<-release
			return
		}
		_, _ = io.WriteString(w, `{"unexpected":`)
	}))
	defer server.Close()
	defer close(release)
	client, err := New(Config{
		BaseURL: server.URL,
		Timeout: 20 * time.Millisecond,
		Meter:   meterProvider.Meter("test"),
		Tracer:  tracerProvider.Tracer("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, parent := tracerProvider.Tracer("caller").Start(t.Context(), "tool-call")
	parentContext := parent.SpanContext()
	var decoded struct {
		OK bool `json:"ok"`
	}
	result, err := client.Do(ctx, Request{
		Method: http.MethodGet,
		Route:  "/sequences/{id}",
		PathParams: map[string]string{
			"id": "7db1118d-57d9-4657-9779-8e1e619b12c4",
		},
	}, &decoded)
	var requestError *RequestError
	if !errors.As(err, &requestError) || requestError.Class != "decode" || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%+v error=%v, want failed decode", result, err)
	}
	if traceparentCount.Load() != 1 {
		t.Fatalf("requests with traceparent = %d, want 1", traceparentCount.Load())
	}
	_, err = client.Do(ctx, Request{Method: http.MethodGet, Route: "/server/state"}, nil)
	if !errors.As(err, &requestError) || requestError.Class != "timeout" {
		t.Fatalf("deadline error = %v, want timeout RequestError", err)
	}
	if traceparentCount.Load() != 2 {
		t.Fatalf("requests with traceparent = %d, want 2", traceparentCount.Load())
	}
	parent.End()

	spans := spanRecorder.Ended()
	var clientSpan bool
	var templatedRouteSeen bool
	for _, span := range spans {
		if span.Name() != "ara.http.request" {
			continue
		}
		clientSpan = true
		if span.Parent().SpanID() != parentContext.SpanID() || span.Parent().TraceID() != parentContext.TraceID() {
			t.Error("client span is not a child of the caller span")
		}
		for _, attr := range span.Attributes() {
			if attr.Key == "http.route" {
				route := attr.Value.AsString()
				if route == "/sequences/{id}" {
					templatedRouteSeen = true
				} else if route != "/server/state" {
					t.Errorf("span route = %q, want normalized route", route)
				}
			}
		}
	}
	if !clientSpan {
		t.Fatal("missing Ara request span")
	}
	if !templatedRouteSeen {
		t.Fatal("span omitted the route template")
	}

	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var requestCount, durationCount uint64
	errorCounts := make(map[string]uint64)
	for _, scope := range metrics.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			switch recorded.Name {
			case "ara.http.requests":
				data, ok := recorded.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("request metric data = %T", recorded.Data)
				}
				for _, point := range data.DataPoints {
					requestCount += uint64(point.Value)
					value, ok := point.Attributes.Value(attribute.Key("error.type"))
					if !ok {
						t.Error("request metric missing error.type")
					} else {
						errorCounts[value.AsString()] += uint64(point.Value)
					}
				}
			case "ara.http.request.duration":
				data, ok := recorded.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("duration metric data = %T", recorded.Data)
				}
				for _, point := range data.DataPoints {
					durationCount += point.Count
				}
			}
		}
	}
	if requestCount != 2 || durationCount != 2 || errorCounts["decode"] != 1 || errorCounts["timeout"] != 1 {
		t.Fatalf("request count=%d duration count=%d error counts=%v, want two measurements, one decode and one timeout", requestCount, durationCount, errorCounts)
	}
}
