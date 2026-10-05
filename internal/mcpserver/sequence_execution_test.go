// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const simulatedCameraStatus = `{"state":"connected","capabilities":{"min_exposure_sec":0.001,"max_exposure_sec":3600,"min_gain":0,"max_gain":0,"min_offset":0,"max_offset":0,"min_bin_x":1,"max_bin_x":4,"min_bin_y":1,"max_bin_y":4}}`

func TestGetSequenceStateReturnsAraObservation(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		state      = `{"sequence_id":"` + sequenceID + `","run_id":"run-01","state":"running","current_instruction_index":2,"instructions_completed":1,"instructions_total":4,"frames_captured":1}`
	)
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sequences/"+sequenceID+"/state" {
			t.Errorf("request = %s %s, want sequence state GET", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, state)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "get_sequence_state",
		Arguments: json.RawMessage(`{"sequence_id":"` + sequenceID + `"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("get_sequence_state = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(state), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("state = %s, want Ara observation %s", encoded, state)
	}
}

func TestStartSequencePreflightsAndReturnsAcceptedState(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30}]}}`
		detail     = `{"id":"` + sequenceID + `","body":` + body + `}`
	)
	var starts atomic.Int64
	var runCompleted atomic.Bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
			_, _ = io.WriteString(w, detail)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
			_, _ = io.WriteString(w, `{"valid":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			_, _ = io.WriteString(w, simulatedCameraStatus)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
			starts.Add(1)
			if got, err := io.ReadAll(r.Body); err != nil || !bytes.Contains(got, []byte(`"dry_run":false`)) {
				t.Errorf("start body = %s, error = %v; want dry_run false", got, err)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"receipt-01"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/state":
			state := "starting"
			if runCompleted.Load() {
				state = "completed"
			}
			_, _ = io.WriteString(w, `{"sequence_id":"`+sequenceID+`","run_id":"`+sequenceID+`","state":"`+state+`"}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "start_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"start-intent-01","sequence_id":"` + sequenceID + `"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("start_sequence = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"outcome":"accepted"`)) || !bytes.Contains(encoded, []byte(`"state":"starting"`)) ||
		!bytes.Contains(encoded, []byte(`"receipt_id":"receipt-01"`)) || starts.Load() != 1 {
		t.Fatalf("start_sequence result = %s, Ara starts = %d; want accepted receipt and observed starting state", encoded, starts.Load())
	}
	if strings.Contains(strings.ToLower(string(encoded)), "completed") {
		t.Fatalf("accepted start was reported as completed: %s", encoded)
	}
	runCompleted.Store(true)
	replayed, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "start_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"start-intent-01","sequence_id":"` + sequenceID + `"}`),
	})
	if err != nil || replayed.IsError {
		t.Fatalf("replayed start_sequence = %#v, error = %v", replayed, err)
	}
	replayBody, err := json.Marshal(replayed.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 1 || !bytes.Contains(replayBody, []byte(`"replayed":true`)) || !bytes.Contains(replayBody, []byte(`"state":"completed"`)) {
		t.Fatalf("replayed terminal start = %s, Ara start requests = %d; want receipt replay without redispatch", replayBody, starts.Load())
	}
}

func TestRunControlUsesExpectedRunIDAndReportsObservedState(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		runID      = "c216e1ba-1929-4c2a-9b14-a8da3777dd42"
	)
	var pauses atomic.Int64
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/state":
			_, _ = io.WriteString(w, `{"sequence_id":"`+sequenceID+`","run_id":"`+runID+`","state":"paused","instructions_completed":1,"instructions_total":3}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/pause":
			pauses.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"operation_id":"pause-receipt"}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "pause_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"pause-intent-01","sequence_id":"` + sequenceID + `","expected_run_id":"` + runID + `"}`),
	})
	if err != nil || result.IsError {
		t.Fatalf("pause_sequence = %#v, error = %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if pauses.Load() != 1 || !bytes.Contains(encoded, []byte(`"outcome":"accepted"`)) || !bytes.Contains(encoded, []byte(`"state":"paused"`)) {
		t.Fatalf("pause result = %s, Ara pause requests = %d", encoded, pauses.Load())
	}
}

func TestRunControlRejectsStaleRunIDBeforeDispatch(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		currentRun = "c216e1ba-1929-4c2a-9b14-a8da3777dd42"
	)
	var dispatched atomic.Bool
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/state" {
			_, _ = io.WriteString(w, `{"sequence_id":"`+sequenceID+`","run_id":"`+currentRun+`","state":"running"}`)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/sequences/"+sequenceID+"/") {
			dispatched.Store(true)
		}
		t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "stop_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"stale-stop-01","sequence_id":"` + sequenceID + `","expected_run_id":"7db1118d-57d9-4657-9779-8e1e619b12c5"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || dispatched.Load() {
		t.Fatalf("stop result = %#v, Ara dispatched = %t; want stale-run rejection before dispatch", result, dispatched.Load())
	}
}

func TestStartSequenceRejectsDisconnectedCameraAndActiveOtherRun(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30}]}}`
	)
	for _, test := range []struct {
		name         string
		cameraStatus string
		sequenceList string
	}{
		{name: "camera disconnected", cameraStatus: `{"connected":false}`, sequenceList: `{"items":[],"next_cursor":null,"has_more":false}`},
		{name: "another run active", cameraStatus: simulatedCameraStatus, sequenceList: `{"items":[{"id":"another-sequence","current_run_state":"running"}],"next_cursor":null,"has_more":false}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var starts atomic.Int64
			session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
					_, _ = io.WriteString(w, test.sequenceList)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
					_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","body":`+body+`}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
					_, _ = io.WriteString(w, `{"valid":true}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
					_, _ = io.WriteString(w, test.cameraStatus)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
					starts.Add(1)
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "start_sequence",
				Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"rejected-start-01","sequence_id":"` + sequenceID + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || starts.Load() != 0 {
				t.Fatalf("start result = %#v, start dispatches = %d; want preflight rejection", result, starts.Load())
			}
		})
	}
}

func TestStartSequenceRejectsFilterSlotMismatch(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	tests := []struct {
		name       string
		filterName string
		position   int
		labels     string
		slots      string
	}{
		{name: "profile label mismatch", filterName: "L", position: 0, labels: `{"labels":["R"]}`, slots: `{"state":"connected","slots":[{"position":0,"name":"Red"}]}`},
		{name: "physical slot unavailable", filterName: "R", position: 1, labels: `{"labels":["L","R"]}`, slots: `{"state":"connected","slots":[{"position":0,"name":"Red"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"schemaVersion":"openastroara-sequence-v1","$type":"%s","Items":{"$values":[{"$type":"%s","Filter":{"_name":%q,"_position":%d}},{"$type":"%s","ExposureTime":30}]}}`,
				sequentialContainerType, switchFilterType, test.filterName, test.position, takeExposureType)
			var starts atomic.Int64
			session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
					_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
					_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","body":`+body+`}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
					_, _ = io.WriteString(w, `{"valid":true}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
					_, _ = io.WriteString(w, simulatedCameraStatus)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/filterwheel":
					_, _ = io.WriteString(w, test.slots)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/profile/filter-wheel/labels":
					_, _ = io.WriteString(w, test.labels)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
					starts.Add(1)
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "start_sequence", Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"filter-check-01","sequence_id":"` + sequenceID + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || starts.Load() != 0 {
				t.Fatalf("start result = %#v, start dispatches = %d; want filter preflight rejection", result, starts.Load())
			}
		})
	}
}

func TestStartSequenceRejectsCameraCapabilityMismatches(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30,"Binning":{"X":1,"Y":1},"Gain":-1,"Offset":-1}]}}`
	)
	tests := []struct {
		name         string
		body         string
		cameraStatus string
	}{
		{name: "exposure above device maximum", body: strings.Replace(body, `"ExposureTime":30`, `"ExposureTime":3601`, 1), cameraStatus: simulatedCameraStatus},
		{name: "binning above device maximum", body: strings.Replace(body, `"X":1`, `"X":5`, 1), cameraStatus: simulatedCameraStatus},
		{name: "gain above device maximum", body: strings.Replace(body, `"Gain":-1`, `"Gain":1`, 1), cameraStatus: simulatedCameraStatus},
		{name: "camera limits unavailable", body: body, cameraStatus: `{"state":"connected","capabilities":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var starts atomic.Int64
			session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
					_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
					_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","body":`+test.body+`}`)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
					_, _ = io.WriteString(w, `{"valid":true}`)
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
					_, _ = io.WriteString(w, test.cameraStatus)
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
					starts.Add(1)
					w.WriteHeader(http.StatusAccepted)
				default:
					t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name:      "start_sequence",
				Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"camera-cap-check-01","sequence_id":"` + sequenceID + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.IsError || starts.Load() != 0 {
				t.Fatalf("start result = %#v, start dispatches = %d; want capability preflight rejection", result, starts.Load())
			}
		})
	}
}

func TestStartSequenceRejectsContinueOnErrorBeforeDispatch(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":0.1,"ContinueOnError":true}]}}`
	)
	var starts atomic.Int64
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
			_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","body":`+body+`}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
			_, _ = io.WriteString(w, `{"valid":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
			starts.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "start_sequence",
		Arguments: json.RawMessage(`{"control_id":"control-01","intent_id":"continue-error-01","sequence_id":"` + sequenceID + `"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || starts.Load() != 0 {
		t.Fatalf("start result = %#v, start dispatches = %d; want ContinueOnError preflight rejection", result, starts.Load())
	}
}

func TestValidateCameraExecutionRequirementsUsesCurrentDeviceLimits(t *testing.T) {
	minExposure, maxExposure := 0.001, 3600.0
	minGain, maxGain := 0, 0
	minOffset, maxOffset := 0, 0
	minBin, maxBin := 1, 4
	capabilities := cameraCapabilities{
		MinExposureSec: &minExposure, MaxExposureSec: &maxExposure,
		MinGain: &minGain, MaxGain: &maxGain, MinOffset: &minOffset, MaxOffset: &maxOffset,
		MinBinX: &minBin, MaxBinX: &maxBin, MinBinY: &minBin, MaxBinY: &maxBin,
	}
	defaultValue := -1
	tests := []struct {
		name        string
		requirement sequenceExposureRequirement
		missing     bool
		wantErr     error
	}{
		{name: "valid boundary and defaults", requirement: sequenceExposureRequirement{Exposure: minExposure, Gain: &defaultValue, Offset: &defaultValue, BinX: 1, BinY: 1}},
		{name: "exposure above camera maximum", requirement: sequenceExposureRequirement{Exposure: maxExposure + 1, BinX: 1, BinY: 1}, wantErr: errInvalidArgument},
		{name: "gain above camera maximum", requirement: sequenceExposureRequirement{Exposure: 1, Gain: new(1), BinX: 1, BinY: 1}, wantErr: errInvalidArgument},
		{name: "offset above camera maximum", requirement: sequenceExposureRequirement{Exposure: 1, Offset: new(1), BinX: 1, BinY: 1}, wantErr: errInvalidArgument},
		{name: "binning above camera maximum", requirement: sequenceExposureRequirement{Exposure: 1, BinX: maxBin + 1, BinY: 1}, wantErr: errInvalidArgument},
		{name: "missing device binning limits", requirement: sequenceExposureRequirement{Exposure: 1, BinX: 1, BinY: 1}, missing: true, wantErr: errRunStateUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := capabilities
			if test.missing {
				current.MaxBinY = nil
			}
			err := validateCameraExecutionRequirements(current, []sequenceExposureRequirement{test.requirement})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("capability check error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestExecutionResultKeepsAcceptanceWhenStateReadIsCanceled(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	instrumentation := toolInstrumentation{
		version: "test", transport: "stdio", logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		observations: &sequenceObservationTracker{seen: make(map[string]struct{}), logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	}
	result := executionResult(ctx, instrumentation, client, "start", "7db1118d-57d9-4657-9779-8e1e619b12c4", MutationReceipt{
		Outcome: ara.OutcomeAccepted, SequenceID: "7db1118d-57d9-4657-9779-8e1e619b12c4",
	})
	if result.Mutation.Outcome != ara.OutcomeAccepted || result.StateObservationError != "cancelled" || len(result.State) != 0 {
		t.Fatalf("execution result = %+v; want accepted preserved with canceled state observation", result)
	}
}

func TestStartSequenceReturnsUnknownWithoutRetryingAmbiguousResponse(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		runID      = "c216e1ba-1929-4c2a-9b14-a8da3777dd42"
		body       = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":30}]}}`
	)
	var starts atomic.Int64
	session, _ := newSequenceAuthoringTestSession(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID:
			_, _ = io.WriteString(w, `{"id":"`+sequenceID+`","body":`+body+`}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/validate":
			_, _ = io.WriteString(w, `{"valid":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/equipment/camera":
			_, _ = io.WriteString(w, simulatedCameraStatus)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/start":
			starts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"title":"response lost after dispatch"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sequences/"+sequenceID+"/state":
			_, _ = io.WriteString(w, `{"sequence_id":"`+sequenceID+`","run_id":"`+runID+`","state":"running"}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	arguments := json.RawMessage(`{"control_id":"control-01","intent_id":"uncertain-start-01","sequence_id":"` + sequenceID + `"}`)
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "start_sequence", Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("uncertain start result = %#v, error = %v; want structured unknown outcome", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"outcome":"unknown"`)) || !bytes.Contains(encoded, []byte(`"error_class":"upstream_error"`)) ||
		!bytes.Contains(encoded, []byte(`"retry_safe":false`)) || !bytes.Contains(encoded, []byte(`"sequence_id":"`+sequenceID+`"`)) ||
		!bytes.Contains(encoded, []byte(`"state":"running"`)) || starts.Load() != 1 {
		t.Fatalf("uncertain start result = %s, Ara starts = %d", encoded, starts.Load())
	}
	replayed, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "start_sequence", Arguments: arguments})
	if err != nil || replayed.IsError {
		t.Fatalf("uncertain start replay = %#v, error = %v", replayed, err)
	}
	if starts.Load() != 1 {
		t.Fatalf("uncertain start replay dispatched again; requests = %d", starts.Load())
	}
}
