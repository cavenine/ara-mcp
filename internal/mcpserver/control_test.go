// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestControlManager_BeginRequiresConfiguredProfile(t *testing.T) {
	var mutations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/server/state" {
			_, _ = io.WriteString(w, `{"current_profile_id":null}`)
			return
		}
		mutations.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := NewControlManager(client, nil, "test", "stdio")

	if _, err := control.Begin(t.Context(), "control-request-01"); err == nil || !strings.Contains(err.Error(), "configured") {
		t.Fatalf("Begin() error = %v, want configured-rig prerequisite", err)
	}
	if got := mutations.Load(); got != 0 {
		t.Fatalf("Ara mutation requests = %d, want no connect without an active profile", got)
	}
	if got := control.Snapshot().ControlOwnership; got != "not_owned" {
		t.Fatalf("control ownership = %q, want not_owned", got)
	}
}

func TestControlManager_RejectedClaimDoesNotBindSocket(t *testing.T) {
	var websocketRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"current_profile_id":"profile-01"}`)
		case "/api/v1/server/connect":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"title":"Connection rejected","detail":"Server in use by human"}`)
		case "/api/v1/ws":
			websocketRequests.Add(1)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := NewControlManager(client, nil, "test", "stdio")
	if _, err := control.Begin(t.Context(), "control-request-01"); err == nil {
		t.Fatal("Begin() error = nil, want existing Ara owner conflict")
	}
	if got := websocketRequests.Load(); got != 0 {
		t.Fatalf("WebSocket requests = %d, want no bind after rejected claim", got)
	}
	if got := control.Snapshot().ControlOwnership; got != "not_owned" {
		t.Fatalf("control ownership = %q, want not_owned", got)
	}
}

func TestControlManager_BeginAndEndControlWithoutStoppingAraWork(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var disconnects atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"current_profile_id":"profile-01"}`)
		case "/api/v1/server/connect":
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, _, err := conn.Read(context.Background()); err == nil {
				t.Error("WebSocket read error = nil, want client close")
			}
		case "/api/v1/server/disconnect":
			disconnects.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := NewControlManager(client, nil, "test", "stdio")
	defer control.Close(context.Background(), "")

	started, err := control.Begin(t.Context(), "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	if started.ControlID == "" || started.WebSocketState != "connected" {
		t.Fatalf("begin result = %+v, want a local control ID and bound socket", started)
	}
	if err := control.Require(started.ControlID); err != nil {
		t.Fatalf("Require(current control ID) = %v, want nil", err)
	}
	if err := control.Require("stale-control-id"); err == nil {
		t.Fatal("Require(stale control ID) = nil, want rejection")
	}
	if err := control.End(t.Context(), started.ControlID, "control-request-02"); err != nil {
		t.Fatal(err)
	}
	if got := control.Snapshot().ControlOwnership; got != "not_owned" {
		t.Fatalf("control ownership after end = %q, want not_owned", got)
	}
	if got := disconnects.Load(); got != 1 {
		t.Fatalf("Ara disconnect requests = %d, want 1", got)
	}
}

func TestControlToolsExposeBeginAndEnd(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"current_profile_id":"profile-01"}`)
		case "/api/v1/server/connect":
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, _, err := conn.Read(context.Background()); err == nil {
				t.Error("WebSocket read error = nil, want client close")
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := NewControlManager(client, nil, "test", "stdio")
	defer control.Close(context.Background(), "")
	mcpServer, err := New(Options{Ara: client, Control: control})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, tool := range tools.Tools {
		seen[tool.Name] = true
	}
	if !seen["begin_control"] || !seen["end_control"] {
		t.Fatalf("control tools missing from discovery: %v", seen)
	}
	begin, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "begin_control"})
	if err != nil || begin.IsError {
		t.Fatalf("begin_control result=%+v error=%v", begin, err)
	}
	encoded, err := json.Marshal(begin.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var result BeginControlResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.ControlID == "" {
		t.Fatal("begin_control omitted control_id")
	}
	ended, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "end_control", Arguments: json.RawMessage(`{"control_id":"` + result.ControlID + `"}`),
	})
	if err != nil || ended.IsError {
		t.Fatalf("end_control result=%+v error=%v", ended, err)
	}
}
