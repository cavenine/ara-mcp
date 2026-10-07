// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAutofocusReadToolsExposeStateCalibrationAndFrame(t *testing.T) {
	noFrame := atomic.Bool{}
	serverHTTP := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/autofocus/state":
			_, _ = io.WriteString(w, `{"state":"running","mode":"classic","phase":"sweep","trigger":"sequence","total_steps":9,"completed_steps":4,"probes":[{"index":4,"phase":"fine","position":1200,"hfr":2.35,"stars":87,"kept":true}],"has_frame":true,"frame_seq":7}`)
		case "/api/v1/autofocus/calibration":
			http.NotFound(w, r)
		case "/api/v1/autofocus/frame":
			if noFrame.Load() {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("X-Frame-Seq", "7")
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
	registerAutofocusTools(server, guideFocusTestInstrumentation(t), client, nil)
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
	toolNames := map[string]bool{}
	for _, tool := range tools.Tools {
		toolNames[tool.Name] = true
	}
	for _, name := range []string{"get_autofocus_state", "get_autofocus_frame", "get_autofocus_calibration"} {
		if !toolNames[name] {
			t.Fatalf("missing read-only tool %q", name)
		}
	}
	if toolNames["cancel_autofocus"] || toolNames["recalibrate_autofocus"] {
		t.Fatal("autofocus mutations registered without a control manager")
	}
	stateResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_autofocus_state"})
	if err != nil || stateResult.IsError {
		t.Fatalf("autofocus state = %+v, %v", stateResult, err)
	}
	stateJSON, err := json.Marshal(stateResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var state ara.AutofocusRun
	if err := json.Unmarshal(stateJSON, &state); err != nil || state.State != "running" || state.FrameSeq != 7 || len(state.Probes) != 1 {
		t.Fatalf("autofocus state = %+v, error = %v", state, err)
	}
	calibrationResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_autofocus_calibration"})
	if err != nil || calibrationResult.IsError || calibrationResult.StructuredContent.(map[string]any)["calibrated"] != false {
		t.Fatalf("uncalibrated state = %+v, %v", calibrationResult, err)
	}
	frameResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_autofocus_frame"})
	if err != nil || frameResult.IsError || len(frameResult.Content) != 1 {
		t.Fatalf("autofocus frame = %+v, %v", frameResult, err)
	}
	image, ok := frameResult.Content[0].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/jpeg" || len(image.Data) != 4 {
		t.Fatalf("autofocus image = %#v", frameResult.Content[0])
	}
	if frameResult.StructuredContent.(map[string]any)["frame_seq"] != float64(7) {
		t.Fatalf("autofocus frame metadata = %#v", frameResult.StructuredContent)
	}
	noFrame.Store(true)
	frameResult, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_autofocus_frame"})
	if err != nil || frameResult.IsError || frameResult.StructuredContent.(map[string]any)["available"] != false {
		t.Fatalf("no autofocus frame = %+v, %v", frameResult, err)
	}
}

func TestAutofocusMutationToolsUseControlAndObserveCancellation(t *testing.T) {
	var control *ControlManager
	var cancelCalls, recalibrateCalls atomic.Int32
	var runState atomic.Value
	runState.Store("running")
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
		case "GET /api/v1/autofocus/state":
			_, _ = io.WriteString(w, `{"state":"`+runState.Load().(string)+`","probes":[],"has_frame":false}`)
		case "POST /api/v1/autofocus/cancel":
			cancelCalls.Add(1)
			if runState.Load().(string) != "running" {
				http.Error(w, `{"title":"not_running"}`, http.StatusConflict)
				return
			}
			runState.Store("cancelled")
			w.WriteHeader(http.StatusAccepted)
		case "POST /api/v1/autofocus/recalibrate":
			recalibrateCalls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "GET /api/v1/autofocus/calibration":
			http.NotFound(w, r)
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
	registerAutofocusTools(server, guideFocusTestInstrumentation(t), client, control)
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
	busyRecalibrate, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "recalibrate_autofocus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "recalibrate-busy"}})
	if err != nil || !busyRecalibrate.IsError || recalibrateCalls.Load() != 0 {
		t.Fatalf("recalibrate during autofocus = %+v, %v; want preflight refusal without Ara mutation", busyRecalibrate, err)
	}
	cancelResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "cancel_autofocus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "cancel-01"}})
	if err != nil || cancelResult.IsError {
		t.Fatalf("cancel autofocus = %+v, %v", cancelResult, err)
	}
	encoded, err := json.Marshal(cancelResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var cancelled AutofocusMutationResult
	if err := json.Unmarshal(encoded, &cancelled); err != nil || cancelled.Mutation.Outcome != ara.OutcomeAccepted || cancelled.State == nil || cancelled.State.State != "cancelled" {
		t.Fatalf("cancel result = %+v, error = %v", cancelled, err)
	}
	replay, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "cancel_autofocus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "cancel-01"}})
	if err != nil || replay.IsError {
		t.Fatalf("replayed cancel = %+v, %v", replay, err)
	}
	encoded, err = json.Marshal(replay.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var replayed AutofocusMutationResult
	if err := json.Unmarshal(encoded, &replayed); err != nil || !replayed.Mutation.Replayed || cancelCalls.Load() != 1 {
		t.Fatalf("replayed cancel = %+v, error = %v, Ara calls = %d", replayed, err, cancelCalls.Load())
	}
	notRunning, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "cancel_autofocus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "cancel-again"}})
	if err != nil || !notRunning.IsError {
		t.Fatalf("cancel with no running autofocus = %+v, %v; want Ara conflict", notRunning, err)
	}
	recalibrateResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "recalibrate_autofocus", Arguments: map[string]any{"control_id": "control-01", "intent_id": "recalibrate-01"}})
	if err != nil || recalibrateResult.IsError {
		t.Fatalf("recalibrate autofocus = %+v, %v", recalibrateResult, err)
	}
	encoded, err = json.Marshal(recalibrateResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var recalibrated AutofocusMutationResult
	if err := json.Unmarshal(encoded, &recalibrated); err != nil || recalibrated.Mutation.Outcome != ara.OutcomeCompleted {
		t.Fatalf("recalibrate result = %+v, error = %v", recalibrated, err)
	}
	if cancelCalls.Load() != 2 || recalibrateCalls.Load() != 1 {
		t.Fatalf("Ara mutation calls: cancel=%d recalibrate=%d", cancelCalls.Load(), recalibrateCalls.Load())
	}
}
