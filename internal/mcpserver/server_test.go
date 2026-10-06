// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestGetServerContext(t *testing.T) {
	var logs bytes.Buffer
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	traceExporter := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(traceExporter))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") == "" {
			t.Error("Ara request omitted logical request ID")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = w.Write([]byte(`{"server_uuid":"test-id","version":"99.0.0","api":"v99","tier":"unknown"}`))
		case "/api/v1/server/versions":
			_, _ = w.Write([]byte(`{"daemon_version":"1.0.0","daemon_git_sha":"abc123","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`))
		case "/api/v1/server/state":
			_, _ = w.Write([]byte(`{"current_profile_id":null,"active_sequence_run":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{
		Ara: client, Version: "test", Logger: slog.New(slog.NewJSONHandler(&logs, nil)),
		Meter: meterProvider.Meter("test"), Tracer: tracerProvider.Tracer("test"),
	})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 12 {
		t.Fatalf("discovered %d tools, want 12", len(tools.Tools))
	}
	for _, tool := range tools.Tools {
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is not marked read-only", tool.Name)
		}
	}

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_server_context"})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool failed: %#v", result.Content)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	serverInfo, ok := got["server"].(map[string]any)
	if !ok || serverInfo["server_uuid"] != "test-id" || serverInfo["version"] != "99.0.0" || serverInfo["api"] != "v99" {
		t.Fatalf("server context = %s, want server UUID test-id", encoded)
	}
	if _, ok := got["versions"]; !ok {
		t.Fatalf("server context omitted version info: %s", encoded)
	}
	if _, ok := got["state"]; !ok {
		t.Fatalf("server context omitted state: %s", encoded)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"event":"tool_call"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"outcome":"success"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"request_id"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"trace_id"`)) ||
		!bytes.Contains(logs.Bytes(), []byte(`"span_id"`)) {
		t.Fatalf("missing structured tool completion log: %s", logs.String())
	}
	spans := traceExporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "mcp.tool.get_server_context" {
		t.Fatalf("tool spans = %+v", spans)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	var toolCalls, inFlight int64
	for _, scope := range metrics.ScopeMetrics {
		for _, item := range scope.Metrics {
			if item.Name != "mcp.tool.calls" && item.Name != "mcp.tool.in_flight" {
				continue
			}
			sum, ok := item.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("tool metric %s data = %T", item.Name, item.Data)
			}
			for _, point := range sum.DataPoints {
				switch item.Name {
				case "mcp.tool.calls":
					outcome, ok := point.Attributes.Value(attribute.Key("mcp.outcome"))
					if ok && outcome.AsString() == "success" {
						toolCalls += point.Value
					}
				case "mcp.tool.in_flight":
					inFlight += point.Value
				}
			}
		}
	}
	if toolCalls != 1 {
		t.Fatalf("successful tool call metric = %d, want 1", toolCalls)
	}
	if inFlight != 0 {
		t.Fatalf("in-flight gauge = %d after completed tool call, want 0", inFlight)
	}
}

func TestReadOnlySequenceTools(t *testing.T) {
	var logs bytes.Buffer
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/sequences":
			if r.URL.Query().Get("limit") != "7" {
				t.Errorf("sequence limit = %q, want 7", r.URL.Query().Get("limit"))
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"s1","name":"M31","current_run_state":{"state":"completed"}}],"next_cursor":null,"has_more":false}`))
		case "/api/v1/sequences/s1":
			_, _ = w.Write([]byte(`{"id":"s1","name":"M31","body":{"$type":"CustomStep","schemaVersion":"openastroara-sequence-v1","unknown":{"keep":1.25}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server, err := New(Options{Ara: client, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	invalid, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_sequences",
		Arguments: json.RawMessage(`{"limit":101}`),
	})
	if err != nil || !invalid.IsError {
		t.Fatalf("invalid sequence limit result = %#v, error = %v", invalid, err)
	}
	if !bytes.Contains(logs.Bytes(), []byte(`"error_class":"invalid_argument"`)) {
		t.Fatalf("invalid tool argument has no bounded error class: %s", logs.String())
	}
	listed, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "list_sequences",
		Arguments: json.RawMessage(`{"limit":7}`),
	})
	if err != nil || listed.IsError {
		t.Fatalf("list_sequences result = %#v, error = %v", listed, err)
	}
	gotList, err := json.Marshal(listed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items []map[string]any `json:"items"`
		More  bool             `json:"has_more"`
	}
	if err := json.Unmarshal(gotList, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0]["id"] != "s1" || page.More {
		t.Fatalf("list_sequences = %s", gotList)
	}

	detail, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_sequence",
		Arguments: json.RawMessage(`{"sequence_id":"s1"}`),
	})
	if err != nil || detail.IsError {
		t.Fatalf("get_sequence result = %#v, error = %v", detail, err)
	}
	gotDetail, err := json.Marshal(detail.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(gotDetail) || !bytes.Contains(gotDetail, []byte(`"$type":"CustomStep"`)) || !bytes.Contains(gotDetail, []byte(`"unknown":{"keep":1.25}`)) {
		t.Fatalf("get_sequence did not preserve opaque body: %s", gotDetail)
	}
}

func TestValidateSequencePreservesOpaqueBody(t *testing.T) {
	body := `{"schemaVersion":"openastroara-sequence-v1","$type":"CustomStep","unknown":{"keep":1.25}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/sequences/validate" {
			t.Errorf("request = %s %s, want POST /api/v1/sequences/validate", r.Method, r.URL.Path)
		}
		var got struct {
			Body json.RawMessage `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode validation request: %v", err)
		}
		var gotTree, wantTree any
		if err := json.Unmarshal(got.Body, &gotTree); err != nil {
			t.Errorf("decode forwarded validation body: %v", err)
		}
		if err := json.Unmarshal([]byte(body), &wantTree); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotTree, wantTree) {
			t.Errorf("validation body = %s, want same opaque tree as %s", got.Body, body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"valid":false,"reason":"unsupported instruction"}`))
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server, err := New(Options{Ara: client, Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "validate_sequence",
		Arguments: json.RawMessage(`{"body":` + body + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		var detail string
		if len(result.Content) > 0 {
			if text, ok := result.Content[0].(*mcp.TextContent); ok {
				detail = text.Text
			}
		}
		t.Fatalf("validate_sequence result = %#v, detail = %q, error = %v", result, detail, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var validation struct {
		Valid  bool   `json:"valid"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(encoded, &validation); err != nil {
		t.Fatal(err)
	}
	if validation.Valid || validation.Reason != "unsupported instruction" {
		t.Fatalf("validation result = %s, want invalid with Ara's reason", encoded)
	}
}

func TestListSequenceTemplatesPreservesOpaqueBodies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sequences/templates" {
			t.Errorf("request = %s %s, want GET /api/v1/sequences/templates", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"name":"lrgb-dso","category":"single-target","description":"LRGB","is_built_in":false,"body":{"schemaVersion":"openastroara-sequence-v1","$type":"SequentialContainer","unknown":{"keep":1.25}}}]`))
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server, err := New(Options{Ara: client, Logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_sequence_templates"})
	if err != nil || result.IsError {
		t.Fatalf("list_sequence_templates result = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"name":"lrgb-dso"`)) ||
		!bytes.Contains(encoded, []byte(`"$type":"SequentialContainer"`)) ||
		!bytes.Contains(encoded, []byte(`"unknown":{"keep":1.25}`)) {
		t.Fatalf("template metadata/body not preserved: %s", encoded)
	}
}

func TestGetRigContextReportsUnselectedDevices(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/server/state":
			_, _ = w.Write([]byte(`{"current_profile_id":null}`))
		case "/api/v1/profiles":
			_, _ = w.Write([]byte(`{"active_id":null,"profiles":[]}`))
		case "/api/v1/profile/site":
			_, _ = w.Write([]byte(`{"latitude":51.5,"longitude":-0.1}`))
		case "/api/v1/profile/imaging-defaults":
			_, _ = w.Write([]byte(`{"gain":100}`))
		case "/api/v1/profile/filter-wheel/labels", "/api/v1/profile/filter-set":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v1/equipment/camera", "/api/v1/equipment/telescope", "/api/v1/equipment/focuser", "/api/v1/equipment/filterwheel":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Ara: client})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_rig_context"})
	if err != nil || result.IsError {
		t.Fatalf("get_rig_context result = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got RigContext
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.CurrentProfileID != nil || got.Site["latitude"] != 51.5 {
		t.Fatalf("rig profile context = %s", encoded)
	}
	for name, device := range got.Devices {
		if device.Available {
			t.Errorf("unselected %s was reported available: %s", name, encoded)
		}
	}
}

func TestGetRigContextUsesAraProfileRepositorySelection(t *testing.T) {
	const profileID = "e1d64755-e2ae-46f1-aa43-c6e67419e1e9"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/state":
			_, _ = w.Write([]byte(`{"current_profile_id":null}`))
		case "/api/v1/profiles":
			_, _ = w.Write([]byte(`{"active_id":"` + profileID + `","profiles":[{"id":"` + profileID + `","name":"test"}]}`))
		case "/api/v1/profile/site":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v1/profile/imaging-defaults":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v1/profile/filter-wheel/labels", "/api/v1/profile/filter-set":
			_, _ = w.Write([]byte(`{}`))
		case "/api/v1/equipment/camera", "/api/v1/equipment/telescope", "/api/v1/equipment/focuser", "/api/v1/equipment/filterwheel":
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := getRigContext(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentProfileID == nil || *got.CurrentProfileID != profileID {
		t.Fatalf("active profile = %v, want %s", got.CurrentProfileID, profileID)
	}
}

func TestAdapterDiagnosticsRemainAvailableWhenAraIsOffline(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, `{"title":"temporarily unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Ara: client, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_adapter_diagnostics"})
	if err != nil || result.IsError {
		t.Fatalf("diagnostics result = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != "test" || got["ara_reachable"] != false || got["transport"] != "stdio" {
		t.Fatalf("diagnostics = %s", encoded)
	}
	if heartbeat, ok := got["last_heartbeat"]; !ok || heartbeat != nil {
		t.Fatalf("unknown heartbeat must be explicit: %s", encoded)
	}
	if reconciliation, ok := got["last_reconciliation"]; !ok || reconciliation != nil {
		t.Fatalf("unknown reconciliation must be explicit: %s", encoded)
	}
	if _, ok := got["process"].(map[string]any); !ok {
		t.Fatalf("diagnostics omitted process sample: %s", encoded)
	}
	second, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_adapter_diagnostics"})
	if err != nil || second.IsError || requests.Load() != 1 {
		t.Fatalf("second diagnostics check = %#v, error=%v, upstream checks=%d; want cached result", second, err, requests.Load())
	}
}

func TestAraHealthCancellationDoesNotCacheAsOffline(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	lastGood := time.Now().UTC()
	health := &araHealth{lastCheck: time.Now().Add(-10 * time.Second), reachable: true, lastGood: lastGood}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := health.check(ctx, client, "cancelled-request"); err != context.Canceled {
		t.Fatalf("cancelled health check error = %v, want context.Canceled", err)
	}
	if requests.Load() != 0 || !health.reachable || health.lastGood != lastGood {
		t.Fatalf("cancellation corrupted cached health: requests=%d health=%+v", requests.Load(), health)
	}
}

func TestToolFailureIsSanitizedAndLoggedOnceAtBoundary(t *testing.T) {
	var logs bytes.Buffer
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"title":"password=private-value"}`))
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Ara: client, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_server_context"})
	if err != nil || !result.IsError {
		t.Fatalf("expected tool error, result=%#v error=%v", result, err)
	}
	response, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(response, []byte("private-value")) || !bytes.Contains(response, []byte("[redacted]")) {
		t.Fatalf("tool response is not sanitized: %s", response)
	}
	if bytes.Contains(logs.Bytes(), []byte("private-value")) ||
		!bytes.Contains(logs.Bytes(), []byte(`"error_class":"upstream_unavailable"`)) ||
		bytes.Count(logs.Bytes(), []byte(`"event":"tool_call"`)) != 1 {
		t.Fatalf("tool failure log is missing bounded classification or leaked detail: %s", logs.String())
	}
}

func TestExpectedSDKCancellationIsNotLoggedAsError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(sdkLogHandler{Handler: slog.NewJSONHandler(&logs, nil)})
	logger.ErrorContext(context.Background(), "server run cancelled", "error", context.Canceled)
	if !bytes.Contains(logs.Bytes(), []byte(`"level":"INFO"`)) ||
		bytes.Contains(logs.Bytes(), []byte(`"level":"ERROR"`)) ||
		bytes.Contains(logs.Bytes(), []byte(`"error":"context canceled"`)) {
		t.Fatalf("expected cancellation log = %s", logs.String())
	}
	logs.Reset()
	logger.Error("upstream failure")
	if !bytes.Contains(logs.Bytes(), []byte(`"level":"ERROR"`)) {
		t.Fatalf("real error was not preserved: %s", logs.String())
	}
}

func TestReadToolConcurrencyIsBounded(t *testing.T) {
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/info" {
			entered <- struct{}{}
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Ara: client})
	if err != nil {
		t.Fatal(err)
	}
	clientSessions := make([]*mcp.ClientSession, 5)
	for i := range clientSessions {
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(t.Context(), serverTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = serverSession.Close() })
		clientSessions[i], err = mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).
			Connect(t.Context(), clientTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = clientSessions[i].Close() })
	}

	results := make(chan error, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		session := clientSessions[i]
		wg.Go(func() {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_server_context"})
			if err != nil {
				results <- err
			} else if result.IsError {
				results <- errReadCapacity
			} else {
				results <- nil
			}
		})
	}
	for range 4 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("four read handlers did not reach Ara")
		}
	}
	busy, err := clientSessions[4].CallTool(t.Context(), &mcp.CallToolParams{Name: "get_server_context"})
	if err != nil || !busy.IsError {
		close(release)
		t.Fatalf("fifth concurrent read = %#v, error=%v; want explicit capacity error", busy, err)
	}
	close(release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("admitted read failed: %v", err)
		}
	}
}
