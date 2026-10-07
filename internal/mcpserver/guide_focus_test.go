// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestGuideFocusToolsExposeStatusAndImage(t *testing.T) {
	noFrameNow := atomic.Bool{}
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/equipment/guider/focus":
			_, _ = io.WriteString(w, `{"active":true,"state":"running","exposure_sec":2,"seq":1,"recent":[],"consecutive_failures":0,"has_frame":true}`)
		case "/api/v1/equipment/guider/focus/frame":
			if noFrameNow.Load() {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
		default:
			t.Errorf("unexpected Ara request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer serverHTTP.Close()
	client, err := ara.New(ara.Config{BaseURL: serverHTTP.URL})
	if err != nil {
		t.Fatal(err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	// Reads are always available; nil control intentionally omits mutations.
	registerGuideFocusTools(server, guideFocusTestInstrumentation(t), client, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	status, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_guide_focus_status"})
	if err != nil || status.IsError {
		t.Fatalf("status = %+v, %v", status, err)
	}
	frame, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_guide_focus_frame"})
	if err != nil || frame.IsError || len(frame.Content) != 1 {
		t.Fatalf("frame = %+v, %v", frame, err)
	}
	image, ok := frame.Content[0].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/jpeg" || len(image.Data) != 4 {
		t.Fatalf("image = %#v", frame.Content[0])
	}
	// Ara's 204 is an ordinary unavailable observation, not an empty/broken image.
	noFrameNow.Store(true)
	noFrame, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_guide_focus_frame"})
	if err != nil || noFrame.IsError || noFrame.StructuredContent.(map[string]any)["available"] != false {
		t.Fatalf("no-frame response = %+v, %v", noFrame, err)
	}
	if len(noFrame.Content) == 1 {
		if _, isImage := noFrame.Content[0].(*mcp.ImageContent); isImage {
			t.Fatalf("204 response unexpectedly contains image content: %+v", noFrame)
		}
	}
	encoded, err := json.Marshal(status.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatalf("status is not JSON: %s", encoded)
	}
}

func TestGuideFocusMutationsUseControlAndAraLease(t *testing.T) {
	var control *ControlManager
	var focusState atomic.Value
	focusState.Store("idle")
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "GET /api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "GET /api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "GET /api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"`+control.clientHostname+`","idle_seconds":1}`)
		case "GET /api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case "GET /api/v1/equipment/guider":
			_, _ = io.WriteString(w, `{"connected":true,"state":"connected"}`)
		case "GET /api/v1/equipment/polaralign/status":
			_, _ = io.WriteString(w, `{"active":false,"state":"idle"}`)
		case "POST /api/v1/equipment/guider/focus/start":
			focusState.Store("running")
			w.WriteHeader(http.StatusAccepted)
		case "POST /api/v1/equipment/guider/focus/stop":
			focusState.Store("stopped")
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/v1/equipment/guider/focus":
			_, _ = io.WriteString(w, `{"active":`+map[string]string{"running": "true", "stopped": "false", "idle": "false"}[focusState.Load().(string)]+`,"state":"`+focusState.Load().(string)+`","exposure_sec":2,"seq":1,"recent":[],"consecutive_failures":0,"has_frame":false}`)
		default:
			t.Errorf("unexpected Ara request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer serverHTTP.Close()
	client, err := ara.New(ara.Config{BaseURL: serverHTTP.URL})
	if err != nil {
		t.Fatal(err)
	}
	control, err = NewControlManager(client, nil, "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	control.phase = "owned"
	control.controlID = "control-01"
	control.identity = controlIdentity{serverUUID: "server-01", serverVersion: "1.0.0", apiVersion: "v1", daemonVersion: "1.0.0", daemonGitSHA: "build-01", apiSurfaces: []ara.APISurface{{Name: "rest", Version: "1.0.0"}}, profileID: "profile-01", resumeToken: "0"}
	control.session = ara.ControlSession{Hostname: control.clientHostname}
	control.mu.Unlock()
	t.Cleanup(control.stop)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	registerGuideFocusTools(server, guideFocusTestInstrumentation(t), client, control)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	start, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "start_guide_focus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "guide-start-01", "exposure_sec": 2.5, "binning": 2}})
	if err != nil || start.IsError {
		t.Fatalf("start guide focus = %+v, %v", start, err)
	}
	encoded, err := json.Marshal(start.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var started guideFocusMutationResult
	if err := json.Unmarshal(encoded, &started); err != nil || started.Mutation.Outcome != ara.OutcomeAccepted || started.Status == nil || !started.Status.Active {
		t.Fatalf("start result = %+v, error = %v", started, err)
	}
	stop, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "stop_guide_focus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "guide-stop-01"}})
	if err != nil || stop.IsError {
		t.Fatalf("stop guide focus = %+v, %v", stop, err)
	}
	encoded, err = json.Marshal(stop.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var stopped guideFocusMutationResult
	if err := json.Unmarshal(encoded, &stopped); err != nil || stopped.Mutation.Outcome != ara.OutcomeCompleted || stopped.Status == nil || stopped.Status.Active || stopped.Status.State != "stopped" {
		t.Fatalf("stop result = %+v, error = %v", stopped, err)
	}
}

func guideFocusTestInstrumentation(t *testing.T) toolInstrumentation {
	t.Helper()
	metrics, err := newMetrics(metricnoop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	return toolInstrumentation{version: "test", transport: "stdio", logger: slog.Default(), tracer: tracenoop.NewTracerProvider().Tracer("test"), metrics: metrics, reads: make(chan struct{}, 4)}
}
