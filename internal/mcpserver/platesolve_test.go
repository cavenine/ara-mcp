// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

func TestPlateSolveToolsExposeSolveCenterAndCancel(t *testing.T) {
	const frameID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var jobCancelled atomic.Bool
	calls := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+" "+r.URL.Path]++
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/platesolve/frames/" + frameID + "/solve":
			_, _ = w.Write([]byte(`{"success":false,"ra":null,"dec":null,"orientation":null,"pixel_scale":null,"search_radius":null}`))
		case "GET /api/v1/sequences":
			_, _ = w.Write([]byte(`{"items":[],"next_cursor":null,"has_more":false}`))
		case "GET /api/v1/server/info":
			_, _ = w.Write([]byte(`{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`))
		case "GET /api/v1/server/versions":
			_, _ = w.Write([]byte(`{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`))
		case "GET /api/v1/server/state":
			_, _ = w.Write([]byte(`{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`))
		case "GET /api/v1/server/session":
			_, _ = w.Write([]byte(`{"connected":true,"hostname":"plate-test","idle_seconds":1}`))
		case "POST /api/v1/platesolve/center":
			if calls[r.Method+" "+r.URL.Path] == 4 {
				http.Error(w, "ambiguous", http.StatusInternalServerError)
				return
			}
			if calls[r.Method+" "+r.URL.Path] == 3 {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"title":"center_in_progress"}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","job_type":"center","state":"queued","done":0,"total":3}`))
		case "GET /api/v1/jobs/job-1":
			state := "running"
			if jobCancelled.Load() {
				state = "cancelled"
			}
			_, _ = w.Write([]byte(`{"job_id":"job-1","job_type":"center","state":"` + state + `","done":1,"total":3}`))
		case "GET /api/v1/equipment/telescope":
			_, _ = w.Write([]byte(`{"state":"connected"}`))
		case "DELETE /api/v1/jobs/job-1":
			jobCancelled.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected upstream request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControlManager(client, nil, "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	control.phase, control.controlID, control.socketActive = "owned", "control", true
	control.identity = controlIdentity{serverUUID: "server-01", serverVersion: "1.0.0", apiVersion: "v1", daemonVersion: "1.0.0", daemonGitSHA: "build-01", apiSurfaces: []ara.APISurface{{Name: "rest", Version: "1.0.0"}}, profileID: "profile-01", resumeToken: "0"}
	control.session = ara.ControlSession{Hostname: "plate-test"}
	control.mu.Unlock()
	t.Cleanup(control.stop)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	metrics, err := newMetrics(metricnoop.NewMeterProvider().Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	registerPlateSolveTools(server, toolInstrumentation{logger: slog.Default(), tracer: tracenoop.NewTracerProvider().Tracer("test"), metrics: metrics, reads: make(chan struct{}, 4)}, client, control)
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
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, tool := range tools.Tools {
		found[tool.Name] = true
	}
	for _, name := range []string{"solve_frame", "center_on_coordinates", "cancel_centering_job"} {
		if !found[name] {
			t.Fatalf("missing tool %q", name)
		}
	}
	if result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "solve_frame", Arguments: map[string]any{"frame_id": frameID}}); err != nil || result.IsError {
		t.Fatalf("solve = %+v, %v", result, err)
	}
	centerArgs := map[string]any{"control_id": "control", "intent_id": "center-1", "ra_hours": 12.5, "dec_degrees": -30.0}
	centerResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "center_on_coordinates", Arguments: centerArgs})
	if err != nil || centerResult.IsError {
		t.Fatalf("center = %+v, %v", centerResult, err)
	}
	joinedArgs := map[string]any{"control_id": "control", "intent_id": "center-2", "ra_hours": 12.5, "dec_degrees": -30.0}
	joinedResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "center_on_coordinates", Arguments: joinedArgs})
	if err != nil || joinedResult.IsError {
		t.Fatalf("same-target join = %+v, %v", joinedResult, err)
	}
	joinedJSON, _ := json.Marshal(joinedResult.StructuredContent)
	if !strings.Contains(string(joinedJSON), `"job_id":"job-1"`) || !strings.Contains(string(joinedJSON), `"outcome":"accepted"`) {
		t.Fatalf("join result = %s", joinedJSON)
	}
	differentArgs := map[string]any{"control_id": "control", "intent_id": "center-3", "ra_hours": 13.0, "dec_degrees": -30.0}
	if result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "center_on_coordinates", Arguments: differentArgs}); err != nil || !result.IsError {
		t.Fatalf("different-target conflict = %+v, %v", result, err)
	}
	unknownArgs := map[string]any{"control_id": "control", "intent_id": "center-4", "ra_hours": 14.0, "dec_degrees": -30.0}
	unknownResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "center_on_coordinates", Arguments: unknownArgs})
	if err != nil || unknownResult.IsError {
		t.Fatalf("unknown center outcome = %+v, %v", unknownResult, err)
	}
	unknownJSON, _ := json.Marshal(unknownResult.StructuredContent)
	if !strings.Contains(string(unknownJSON), `"outcome":"unknown"`) {
		t.Fatalf("unknown result = %s", unknownJSON)
	}
	cancelled, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "cancel_centering_job", Arguments: map[string]any{"control_id": "control", "intent_id": "cancel-1", "job_id": "job-1"}})
	if err != nil || cancelled.IsError {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	cancelJSON, _ := json.Marshal(cancelled.StructuredContent)
	if !strings.Contains(string(cancelJSON), `"outcome":"accepted"`) || !strings.Contains(string(cancelJSON), `"state":"cancelled"`) {
		t.Fatalf("cancel result = %s; want request acceptance plus observed cancelled job", cancelJSON)
	}
	if calls["POST /api/v1/platesolve/center"] != 4 || calls["DELETE /api/v1/jobs/job-1"] != 1 {
		t.Fatalf("upstream calls = %v", calls)
	}
}

func TestCenterCoordinateValidation(t *testing.T) {
	for _, input := range []CenterInput{
		{RAHours: -1, DecDegrees: 0}, {RAHours: 24, DecDegrees: 0}, {RAHours: 1, DecDegrees: 91},
	} {
		if err := validateCenterInput(input); err == nil {
			t.Fatalf("accepted invalid coordinates %+v", input)
		}
	}
}
