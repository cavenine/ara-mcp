// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestFaultToolsReadPagesAndDetailsWithoutControl(t *testing.T) {
	const faultID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		switch r.URL.Path {
		case "/api/v1/faults":
			if r.URL.Query().Get("limit") != "25" || r.URL.Query().Get("cursor") != "25" || r.URL.Query().Get("unresolvedOnly") != "true" {
				t.Errorf("fault query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"`+faultID+`","detected_utc":"2026-10-06T00:00:00Z","equipment_type":"camera","fault_type":"disconnected","affected_frames":[]}],"next_cursor":"50","has_more":true}`)
		case "/api/v1/faults/" + faultID:
			_, _ = io.WriteString(w, `{"id":"`+faultID+`","detected_utc":"2026-10-06T00:00:00Z","equipment_type":"camera","fault_type":"disconnected","affected_frames":[]}`)
		case "/api/v1/faults/c15e5138-12f0-4c41-8a43-ed79ef527e12":
			http.NotFound(w, r)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := newMetrics(metricnoop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	mcpServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registerFaultTools(mcpServer, toolInstrumentation{version: "test", transport: "stdio", logger: slog.Default(), tracer: tracenoop.NewTracerProvider().Tracer("test"), metrics: metrics, reads: make(chan struct{}, 4)}, client)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	listed, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_faults", Arguments: map[string]any{"limit": 25, "cursor": "25", "unresolved_only": true}})
	if err != nil || listed.IsError {
		t.Fatalf("list_faults = %+v, error = %v", listed, err)
	}
	body, err := json.Marshal(listed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var page ara.FaultPage
	if err := json.Unmarshal(body, &page); err != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == nil || *page.NextCursor != "50" {
		t.Fatalf("fault page = %+v, error = %v", page, err)
	}
	detail, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_fault", Arguments: map[string]any{"fault_id": faultID}})
	if err != nil || detail.IsError {
		t.Fatalf("get_fault = %+v, error = %v", detail, err)
	}
	body, err = json.Marshal(detail.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var fault ara.Fault
	if err := json.Unmarshal(body, &fault); err != nil || fault.ID != faultID || fault.FaultType != "disconnected" {
		t.Fatalf("fault = %+v, error = %v", fault, err)
	}
	missing, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_fault", Arguments: map[string]any{"fault_id": "c15e5138-12f0-4c41-8a43-ed79ef527e12"}})
	if err != nil || !missing.IsError {
		t.Fatalf("missing get_fault = %+v, error = %v; want MCP tool error", missing, err)
	}
}
