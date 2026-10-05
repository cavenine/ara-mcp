// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestLiveAraSequenceStartAndStateWithPinnedOmniSim(t *testing.T) {
	baseURL := os.Getenv("ARA_MCP_LIVE_ARA_URL")
	if baseURL == "" {
		t.Skip("set ARA_MCP_LIVE_ARA_URL to run the opt-in sequence execution check")
	}
	client, err := ara.New(ara.Config{BaseURL: baseURL, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _, err := client.ListProfilesWithRequestID(t.Context(), "live-t06-profile")
	if err != nil {
		t.Fatal(err)
	}
	profileName := ""
	if profiles.ActiveID != nil {
		for _, profile := range profiles.Profiles {
			if profile.ID == *profiles.ActiveID {
				profileName = profile.Name
			}
		}
	}
	if profileName != "ara-mcp-t06-omnisim" {
		t.Skip("the dedicated ara-mcp-t06-omnisim profile is not active; leaving other profiles untouched")
	}
	cameraBody, _, err := client.GetDeviceStatusWithRequestID(t.Context(), ara.DeviceCamera, "live-t06-camera")
	if err != nil {
		t.Skipf("simulated camera is unavailable: %v", err)
	}
	var camera struct {
		DeviceID string `json:"device_id"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(cameraBody, &camera); err != nil {
		t.Fatal(err)
	}
	if camera.DeviceID != "fe4996a4-2165-4602-9255-8b9362c5f498" || camera.State != "connected" {
		t.Skip("the selected profile does not point to the pinned OmniSim camera")
	}
	storageBody, _, err := client.GetProfileStorageWithRequestID(t.Context(), "live-t06-storage")
	if err != nil {
		t.Fatal(err)
	}
	var storage struct {
		SaveDirectory string `json:"save_directory"`
	}
	if err := json.Unmarshal(storageBody, &storage); err != nil {
		t.Fatal(err)
	}
	if storage.SaveDirectory != "/tmp/ara-mcp-t06-captures" {
		t.Skip("the selected test profile does not use the disposable capture directory")
	}
	sessionInfo, _, err := client.GetServerSessionWithRequestID(t.Context(), "live-t06-owner")
	if err != nil {
		t.Fatal(err)
	}
	if sessionInfo.Connected {
		t.Skip("Ara already has a control-session owner; leaving it undisturbed")
	}
	control, err := NewControlManager(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "integration", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Options{Ara: client, Control: control, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "t06-live-check", Version: "1"}, nil).
		Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	controlID := ""
	sequenceID := ""
	sequenceIDs := make([]string, 0, 2)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if controlID != "" {
			if sequenceID != "" {
				if stateResult, callErr := clientSession.CallTool(cleanup, &mcp.CallToolParams{
					Name: "get_sequence_state", Arguments: json.RawMessage(`{"sequence_id":"` + sequenceID + `"}`),
				}); callErr == nil && !stateResult.IsError {
					state, runID := liveRunIdentity(stateResult.StructuredContent)
					if runID != "" && activeRunState(state) {
						_, _ = clientSession.CallTool(cleanup, &mcp.CallToolParams{
							Name:      "abort_sequence",
							Arguments: json.RawMessage(`{"control_id":"` + controlID + `","intent_id":"live-t06-cleanup-abort","sequence_id":"` + sequenceID + `","expected_run_id":"` + runID + `"}`),
						})
					}
				}
			}
			if err := control.End(cleanup, controlID, "live-t06-cleanup-end"); err != nil {
				t.Errorf("release live T06 control session: %v", err)
			}
			if err := control.Close(cleanup, "live-t06-cleanup-close"); err != nil {
				t.Errorf("close live T06 control manager: %v", err)
			}
		}
		for _, id := range sequenceIDs {
			if !waitForAraTerminal(cleanup, client, id) {
				t.Errorf("Ara run for sequence %s did not become terminal during cleanup", id)
				continue
			}
			if _, err := client.DeleteSequenceWithRequestID(cleanup, id, "live-t06-cleanup-delete"); err != nil {
				t.Errorf("delete live T06 test sequence %s: %v", id, err)
			}
		}
	}()

	begin := liveCallTool(t, clientSession, "begin_control", `{}`)
	controlID = liveString(begin, "control_id")
	if controlID == "" {
		t.Fatalf("begin_control result omitted control_id: %#v", begin)
	}
	const body = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Name":"ara-mcp T06 simulated exposure","Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":0.1,"ImageType":"LIGHT","Binning":{"X":1,"Y":1},"Gain":-1,"Offset":-1}]}}`
	created := liveCallTool(t, clientSession, "create_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-create","name":"ara-mcp T06 OmniSim smoke","body":`+body+`}`)
	var saved struct {
		Mutation MutationReceipt `json:"mutation"`
	}
	decodeLiveContent(t, created, &saved)
	sequenceID = saved.Mutation.SequenceID
	if sequenceID == "" {
		t.Fatalf("create_sequence omitted sequence_id: %#v", created)
	}
	sequenceIDs = append(sequenceIDs, sequenceID)
	started := liveCallTool(t, clientSession, "start_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-start","sequence_id":"`+sequenceID+`"}`)
	var execution SequenceExecutionResult
	decodeLiveContent(t, started, &execution)
	if execution.Mutation.Outcome != ara.OutcomeAccepted {
		t.Fatalf("start_sequence outcome = %q, want accepted; result=%#v", execution.Mutation.Outcome, started)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		stateResult, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
			Name: "get_sequence_state", Arguments: json.RawMessage(`{"sequence_id":"` + sequenceID + `"}`),
		})
		if err == nil && !stateResult.IsError {
			state, _ := liveRunIdentity(stateResult.StructuredContent)
			if state == "completed" {
				break
			}
			if state == "failed" || state == "stopped" {
				t.Fatalf("simulated run ended in %q: %#v", state, stateResult.StructuredContent)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Ara run did not reach completed state before timeout: %v", ctx.Err())
		case <-ticker.C:
		}
	}

	const longBody = `{"schemaVersion":"openastroara-sequence-v1","$type":"OpenAstroAra.Sequencer.Container.SequentialContainer, OpenAstroAra.Sequencer","Name":"ara-mcp T06 lifecycle v2","Conditions":{"$values":[{"$type":"OpenAstroAra.Sequencer.Conditions.LoopCondition, OpenAstroAra.Sequencer","Iterations":1000}]},"Items":{"$values":[{"$type":"NINA.Sequencer.SequenceItem.Imaging.TakeExposure, NINA.Sequencer","ExposureTime":1,"ImageType":"LIGHT","Binning":{"X":1,"Y":1},"Gain":-1,"Offset":-1}]}}`
	longPlan := liveCallTool(t, clientSession, "create_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-lifecycle-create-v2","name":"ara-mcp T06 OmniSim lifecycle v2","body":`+longBody+`}`)
	var longSaved struct {
		Mutation MutationReceipt `json:"mutation"`
	}
	decodeLiveContent(t, longPlan, &longSaved)
	sequenceID = longSaved.Mutation.SequenceID
	if sequenceID == "" {
		t.Fatalf("lifecycle create omitted sequence_id: %#v", longPlan)
	}
	sequenceIDs = append(sequenceIDs, sequenceID)

	startLiveSequence(t, clientSession, controlID, sequenceID, "ara-mcp-t06-omnisim-lifecycle-start-1")
	runID := waitForLiveRunState(t, clientSession, sequenceID, "running", 15*time.Second)
	paused := liveCallTool(t, clientSession, "pause_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-pause","sequence_id":"`+sequenceID+`","expected_run_id":"`+runID+`"}`)
	assertLiveAccepted(t, paused, "pause_sequence")
	waitForLiveRunState(t, clientSession, sequenceID, "paused", 15*time.Second)
	resumed := liveCallTool(t, clientSession, "resume_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-resume","sequence_id":"`+sequenceID+`","expected_run_id":"`+runID+`"}`)
	assertLiveAccepted(t, resumed, "resume_sequence")
	waitForLiveRunState(t, clientSession, sequenceID, "running", 15*time.Second)
	stopped := liveCallTool(t, clientSession, "stop_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-stop","sequence_id":"`+sequenceID+`","expected_run_id":"`+runID+`"}`)
	assertLiveAccepted(t, stopped, "stop_sequence")
	waitForLiveRunState(t, clientSession, sequenceID, "stopped", 15*time.Second)

	startLiveSequence(t, clientSession, controlID, sequenceID, "ara-mcp-t06-omnisim-lifecycle-start-2")
	secondRunID := waitForLiveRunState(t, clientSession, sequenceID, "running", 15*time.Second)
	aborted := liveCallTool(t, clientSession, "abort_sequence", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t06-omnisim-abort","sequence_id":"`+sequenceID+`","expected_run_id":"`+secondRunID+`"}`)
	assertLiveAccepted(t, aborted, "abort_sequence")
	waitForLiveRunState(t, clientSession, sequenceID, "stopped", 15*time.Second)
}

func liveCallTool(t *testing.T, session *mcp.ClientSession, name, arguments string) map[string]any {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(arguments)})
	if err != nil || result.IsError {
		content, _ := json.Marshal(result.Content)
		t.Fatalf("%s result = %#v, content = %s, error = %v", name, result, content, err)
	}
	var output map[string]any
	decodeLiveContent(t, result.StructuredContent, &output)
	return output
}

func decodeLiveContent(t *testing.T, content any, target any) {
	t.Helper()
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("decode MCP result %s: %v", encoded, err)
	}
}

func liveString(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func liveRunIdentity(content any) (state, runID string) {
	encoded, err := json.Marshal(content)
	if err != nil {
		return "", ""
	}
	var observation struct {
		State string `json:"state"`
		RunID string `json:"run_id"`
	}
	if json.Unmarshal(encoded, &observation) != nil {
		return "", ""
	}
	return observation.State, observation.RunID
}

func startLiveSequence(t *testing.T, session *mcp.ClientSession, controlID, sequenceID, intentID string) {
	t.Helper()
	result := liveCallTool(t, session, "start_sequence", `{"control_id":"`+controlID+`","intent_id":"`+intentID+`","sequence_id":"`+sequenceID+`"}`)
	var execution SequenceExecutionResult
	decodeLiveContent(t, result, &execution)
	if execution.Mutation.Outcome != ara.OutcomeAccepted {
		t.Fatalf("start_sequence outcome = %q, want accepted: %#v", execution.Mutation.Outcome, result)
	}
}

func assertLiveAccepted(t *testing.T, result map[string]any, tool string) {
	t.Helper()
	var execution SequenceExecutionResult
	decodeLiveContent(t, result, &execution)
	if execution.Mutation.Outcome != ara.OutcomeAccepted {
		t.Fatalf("%s outcome = %q, want accepted: %#v", tool, execution.Mutation.Outcome, result)
	}
}

func waitForLiveRunState(t *testing.T, session *mcp.ClientSession, sequenceID, expected string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	lastState := "unknown"
	for {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name: "get_sequence_state", Arguments: json.RawMessage(`{"sequence_id":"` + sequenceID + `"}`),
		})
		if err == nil && !result.IsError {
			state, runID := liveRunIdentity(result.StructuredContent)
			if state != "" {
				lastState = state
			}
			if state == expected {
				if runID == "" {
					t.Fatalf("observed %s state without run_id", expected)
				}
				return runID
			}
			if state == "failed" || state == "stopped" || state == "completed" {
				t.Fatalf("Ara run reached %q while waiting for %q", state, expected)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Ara run did not reach %q before timeout (last state %q): %v", expected, lastState, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForAraTerminal(ctx context.Context, client *ara.Client, sequenceID string) bool {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, _, err := client.GetSequenceStateWithRequestID(ctx, sequenceID, "live-t06-cleanup-state")
		if err == nil {
			var observation sequenceRun
			if json.Unmarshal(state, &observation) == nil && terminalRunState(observation.State) {
				return true
			}
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}
