// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCaptureExposureUsesAraAndReturnsAcceptance(t *testing.T) {
	var dispatched int
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			_, _ = io.WriteString(w, `{"state":"connected","capabilities":{"min_exposure_sec":0.001,"max_exposure_sec":3600,"min_gain":0,"max_gain":10,"min_offset":0,"max_offset":10,"min_bin_x":1,"max_bin_x":4,"min_bin_y":1,"max_bin_y":4,"sensor_width":800,"sensor_height":600}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/equipment/camera/exposure":
			dispatched++
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"frame_id":"frame-01","preview_url":"/frames/frame-01/preview","exposure_sec":2,"captured_at":"2026-10-05T00:00:00Z"}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "capture_exposure",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"capture-01","exposure_sec":2,"bin_x":1,"bin_y":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || dispatched != 1 {
		t.Fatalf("capture_exposure result = %#v, Ara dispatches = %d", result, dispatched)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"outcome":"accepted"`) || !strings.Contains(string(encoded), `"frame_id":"frame-01"`) || strings.Contains(string(encoded), `"outcome":"completed"`) {
		t.Fatalf("capture result = %s, want accepted receipt and frame ID", encoded)
	}
}

func TestCaptureExposureRejectsOutOfRangeExposureBeforeDispatch(t *testing.T) {
	var dispatched bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/exposure") {
			dispatched = true
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences" {
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera" {
			_, _ = io.WriteString(w, `{"state":"connected","capabilities":{"min_exposure_sec":0.1,"max_exposure_sec":10,"min_bin_x":1,"max_bin_x":2,"min_bin_y":1,"max_bin_y":2,"sensor_width":800,"sensor_height":600}}`)
			return
		}
		http.NotFound(w, r)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "capture_exposure",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"capture-range-01","exposure_sec":20}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched {
		t.Fatalf("out-of-range capture = %#v, dispatched = %t", result, dispatched)
	}
}

func TestManualEquipmentActionRejectsActiveSequence(t *testing.T) {
	var dispatched bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[{"current_run_state":"paused"}],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			_, _ = io.WriteString(w, `{"state":"connected","capabilities":{"min_exposure_sec":0.001,"max_exposure_sec":3600,"min_gain":0,"max_gain":10,"min_offset":0,"max_offset":10,"min_bin_x":1,"max_bin_x":4,"min_bin_y":1,"max_bin_y":4}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/equipment/camera/exposure":
			dispatched = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "capture_exposure",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"capture-active-01","exposure_sec":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched {
		t.Fatalf("active-run capture = %#v, dispatched = %t; want conflict without Ara action", result, dispatched)
	}
}

func TestCoolerActionReportsUnsupportedCapabilityWithoutDispatch(t *testing.T) {
	var dispatched bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			_, _ = io.WriteString(w, `{"state":"connected","capabilities":{"has_cooler":false,"can_set_temperature":false}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/equipment/camera/cooler":
			dispatched = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "set_camera_cooler",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"cooler-unsupported-01","enabled":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched {
		t.Fatalf("unsupported cooler result = %#v, dispatched = %t", result, dispatched)
	}
	if len(result.Content) == 0 {
		t.Fatal("unsupported capability error omitted details")
	}
}

func TestCameraActionReportsDisconnectedDeviceWithoutDispatch(t *testing.T) {
	var dispatched bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/equipment/camera/cooler":
			dispatched = true
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "set_camera_cooler",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"cooler-disconnected-01","enabled":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched || len(result.Content) == 0 {
		t.Fatalf("disconnected camera result = %#v, dispatched = %t", result, dispatched)
	}
}

func TestGuiderActionsUseAraRoutesAndReturnAcceptance(t *testing.T) {
	counts := map[string]int{}
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/guider":
			_, _ = io.WriteString(w, `{"state":"connected","runtime":{"state":"stopped"}}`)
		case r.Method == http.MethodPost && (r.URL.Path == "/api/v1/equipment/guider/start" || r.URL.Path == "/api/v1/equipment/guider/stop" || r.URL.Path == "/api/v1/equipment/guider/dither"):
			counts[r.URL.Path]++
			if r.URL.Path == "/api/v1/equipment/guider/dither" && r.URL.Query().Get("pixels") != "2.5" {
				t.Errorf("dither pixels = %q", r.URL.Query().Get("pixels"))
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"guide-op-01","operation_type":"guider.action","accepted_utc":"2026-10-05T00:00:00Z"}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	calls := []struct{ name, args, route string }{
		{"start_guiding", `{"control_id":"control-01","intent_id":"guide-start-01"}`, "/api/v1/equipment/guider/start"},
		{"stop_guiding", `{"control_id":"control-01","intent_id":"guide-stop-01"}`, "/api/v1/equipment/guider/stop"},
		{"dither_guiding", `{"control_id":"control-01","intent_id":"guide-dither-01","pixels":2.5}`, "/api/v1/equipment/guider/dither"},
	}
	for _, call := range calls {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: call.name, Arguments: json.RawMessage(call.args)})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s result = %#v", call.name, result)
		}
		encoded, err := json.Marshal(result.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), `"outcome":"accepted"`) || !strings.Contains(string(encoded), `"receipt_id":"guide-op-01"`) {
			t.Errorf("%s result = %s, want accepted operation receipt", call.name, encoded)
		}
		if counts[call.route] != 1 {
			t.Errorf("requests to %s = %d, want 1", call.route, counts[call.route])
		}
	}
}

func TestEmergencyStopReturnsAraRungsAndReplaysWithoutRedispatch(t *testing.T) {
	var stops int
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/server/emergency-stop" {
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		stops++
		_, _ = io.WriteString(w, `{"already_in_progress":false,"runs_aborted":1,"exposure_aborted":true,"guiding_stopped":false,"park_requested":true,"flat_panel_light_off":false,"failed_rungs":[]}`)
	})
	call := func() *mcp.CallToolResult {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
			Name:      "emergency_stop",
			Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"emergency-01"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := call()
	firstJSON, err := json.Marshal(first.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if first.IsError || !strings.Contains(string(firstJSON), `"outcome":"completed"`) || !strings.Contains(string(firstJSON), `"runs_aborted":1`) {
		t.Fatalf("emergency stop = %s", firstJSON)
	}
	replayed := call()
	replayJSON, err := json.Marshal(replayed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.IsError || !strings.Contains(string(replayJSON), `"replayed":true`) || strings.Contains(string(replayJSON), `"runs_aborted":1`) || stops != 1 {
		t.Fatalf("replayed emergency stop = %s, dispatches = %d", replayJSON, stops)
	}
}
