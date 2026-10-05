// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package httpmcp

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHandlerServesSDKProtocolWithBearerAuthAndOriginProtection(t *testing.T) {
	var logs bytes.Buffer
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	traceExporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(traceExporter))
	t.Cleanup(func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	handler, err := Handler(server, "0123456789abcdef0123456789abcdef", nil, slog.New(slog.NewJSONHandler(&logs, nil)),
		meterProvider.Meter("test"), tracerProvider.Tracer("test"))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	rawRequest, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp", strings.NewReader(`{}`))
	rawRequest.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	rawRequest.Header.Set("Content-Type", "application/json")
	rawResponse, err := http.DefaultClient.Do(rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	rawResponse.Body.Close()
	if rawResponse.StatusCode == http.StatusUnauthorized {
		t.Fatal("configured bearer token was rejected")
	}

	clientTransport := &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: "0123456789abcdef0123456789abcdef"}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ping" {
		t.Fatalf("tools = %#v", tools.Tools)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	request.Header.Set("X-Request-ID", "caller-controlled")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want %d", response.Code, http.StatusForbidden)
	}
	for _, test := range []struct {
		name, method, path string
		want               int
	}{
		{name: "unmatched route", method: http.MethodGet, path: "/missing", want: http.StatusNotFound},
		{name: "unsupported method", method: http.MethodPut, path: "/mcp", want: http.StatusMethodNotAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var requests, active int64
	for _, scope := range metrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != "mcp.http.requests" && item.Name != "mcp.http.in_flight" {
				continue
			}
			sum, ok := item.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s data = %T", item.Name, item.Data)
			}
			for _, point := range sum.DataPoints {
				if item.Name == "mcp.http.requests" {
					requests += point.Value
				} else {
					active += point.Value
				}
			}
		}
	}
	if requests < 3 || active != 0 {
		t.Fatalf("HTTP requests=%d in_flight=%d; want at least 3 and 0", requests, active)
	}
	if spans := traceExporter.GetSpans(); len(spans) == 0 {
		t.Fatal("HTTP request spans were not recorded")
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"component":"mcp_http"`)) || !bytes.Contains(logs.Bytes(), []byte(`"route":"unmatched"`)) || !bytes.Contains(logs.Bytes(), []byte(`"http_status":404`)) || !bytes.Contains(logs.Bytes(), []byte(`"http_status":405`)) || bytes.Contains(logs.Bytes(), []byte("caller-controlled")) {
		t.Fatalf("MCP HTTP logs missing component or trusted request ID handling: %s", logs.String())
	}
}

func TestHandlerRejectsMissingBearerToken(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	handler, err := Handler(server, "0123456789abcdef0123456789abcdef", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestClosingOneHTTPClientLeavesOtherSessionUsable(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "ping", Description: "ping"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
		return nil, struct{}{}, nil
	})
	handler, err := Handler(server, "0123456789abcdef0123456789abcdef", nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	connect := func() *mcp.ClientSession {
		client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1"}, nil)
		session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
			Endpoint:   httpServer.URL + "/mcp",
			HTTPClient: &http.Client{Transport: bearerTransport{token: "0123456789abcdef0123456789abcdef"}},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	first, second := connect(), connect()
	t.Cleanup(func() { _ = second.Close() })
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	tools, err := second.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ping" {
		t.Fatalf("second client's tools = %#v", tools.Tools)
	}
}

func TestHandlerRecordsCommittedPanicAsTransportFault(t *testing.T) {
	var logs bytes.Buffer
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	endpoint := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: partial\n\n")
		w.(http.Flusher).Flush()
		panic("sensitive panic detail")
	})
	handler, err := composeHandler(endpoint, "0123456789abcdef0123456789abcdef", nil,
		slog.New(slog.NewJSONHandler(&logs, nil)), meterProvider.Meter("test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "data: partial") || strings.Contains(response.Body.String(), "Internal Server Error") {
		t.Fatalf("committed stream = status %d body %q", response.Code, response.Body.String())
	}
	if !strings.Contains(logs.String(), `"event":"http_fault"`) || strings.Contains(logs.String(), "sensitive panic detail") {
		t.Fatalf("fault was not safely logged: %s", logs.String())
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var requestErrors, faults int64
	for _, scope := range metrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			sum, ok := item.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, point := range sum.DataPoints {
				for _, attr := range point.Attributes.ToSlice() {
					if item.Name == "mcp.http.requests" && attr.Key == "mcp.outcome" && attr.Value.AsString() == "error" {
						requestErrors += point.Value
					}
				}
				if item.Name == "mcp.http.faults" {
					faults += point.Value
				}
			}
		}
	}
	if requestErrors != 1 || faults != 1 {
		t.Fatalf("request error count = %d, fault count = %d; want 1 each", requestErrors, faults)
	}
}

type bearerTransport struct{ token string }

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(request)
}
