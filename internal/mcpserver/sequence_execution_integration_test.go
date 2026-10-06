// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
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
	var control *ControlManager
	var serverSession *mcp.ServerSession
	var clientSession *mcp.ClientSession
	var stdioCommand *exec.Cmd
	var stdioStderr *bytes.Buffer
	liveMCPURL := os.Getenv("ARA_MCP_LIVE_MCP_URL")
	liveMCPToken := os.Getenv("ARA_MCP_LIVE_MCP_TOKEN")
	restartHost := os.Getenv("ARA_MCP_LIVE_SYSTEMD_RESTART_HOST")
	if restartHost != "" && liveMCPURL == "" {
		t.Fatal("ARA_MCP_LIVE_SYSTEMD_RESTART_HOST requires ARA_MCP_LIVE_MCP_URL")
	}
	if liveMCPURL != "" {
		if liveMCPToken == "" {
			t.Fatal("set ARA_MCP_LIVE_MCP_TOKEN when ARA_MCP_LIVE_MCP_URL is set")
		}
		clientSession, err = connectLiveHTTP(t.Context(), liveMCPURL, liveMCPToken)
	} else if os.Getenv("ARA_MCP_LIVE_MCP_STDIO") != "" {
		stdioCommand, stdioStderr, clientSession = startLiveStdioSession(t, baseURL)
	} else {
		control, err = NewControlManager(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "integration", "stdio", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		server, err := New(Options{Ara: client, Control: control, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		if err != nil {
			t.Fatal(err)
		}
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		serverSession, err = server.Connect(t.Context(), serverTransport, nil)
		if err == nil {
			clientSession, err = mcp.NewClient(&mcp.Implementation{Name: "t06-live-check", Version: "1"}, nil).
				Connect(t.Context(), clientTransport, nil)
		}
	}
	if err != nil {
		if stdioCommand != nil && stdioCommand.Process != nil {
			_ = stdioCommand.Process.Kill()
			_ = stdioCommand.Wait()
		}
		t.Fatal(err)
	}
	defer func() {
		if clientSession != nil {
			_ = clientSession.Close()
		}
		if serverSession != nil {
			_ = serverSession.Close()
		}
		if stdioCommand != nil {
			if err := stdioCommand.Wait(); err != nil {
				t.Errorf("live stdio adapter exited: %v; stderr=%s", err, stdioStderr.String())
			}
		}
	}()

	controlID := ""
	sequenceID := ""
	sequenceIDs := make([]string, 0, 2)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if controlID == "" {
			if control != nil {
				begin, err := control.Begin(cleanup, "live-t06-cleanup-begin")
				if err != nil {
					t.Errorf("begin control for live T06 cleanup: %v", err)
				} else {
					controlID = begin.ControlID
				}
			} else if clientSession != nil {
				result, err := clientSession.CallTool(cleanup, &mcp.CallToolParams{Name: "begin_control", Arguments: json.RawMessage(`{}`)})
				if err != nil || result.IsError {
					t.Errorf("begin control for live T10 cleanup: result=%#v err=%v", result, err)
				} else if encoded, marshalErr := json.Marshal(result.StructuredContent); marshalErr != nil {
					t.Errorf("encode live T10 cleanup control result: %v", marshalErr)
				} else {
					var begin struct {
						ControlID string `json:"control_id"`
					}
					if err := json.Unmarshal(encoded, &begin); err != nil {
						t.Errorf("decode live T10 cleanup control result: %v", err)
					} else {
						controlID = begin.ControlID
					}
				}
			}
		}
		if controlID != "" && clientSession == nil {
			t.Error("cannot reconcile live simulator work without an MCP client session")
		}
		if controlID != "" {
			if sequenceID != "" && clientSession != nil {
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
			if control != nil {
				if err := control.End(cleanup, controlID, "live-t06-cleanup-end"); err != nil {
					t.Errorf("release live T06 control session: %v", err)
				}
				if err := control.Close(cleanup, "live-t06-cleanup-close"); err != nil {
					t.Errorf("close live T06 control manager: %v", err)
				}
			} else if clientSession != nil {
				result, err := clientSession.CallTool(cleanup, &mcp.CallToolParams{
					Name: "end_control", Arguments: json.RawMessage(`{"control_id":"` + controlID + `"}`),
				})
				if err != nil || result.IsError {
					t.Errorf("release live T10 HTTP control session: result=%#v err=%v", result, err)
				}
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
	readBack := liveCallTool(t, clientSession, "get_sequence", `{"sequence_id":"`+sequenceID+`"}`)
	if liveString(readBack, "id") != sequenceID {
		t.Fatalf("get_sequence returned ID %q, want saved ID %q", liveString(readBack, "id"), sequenceID)
	}
	missing, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "get_sequence", Arguments: json.RawMessage(`{"sequence_id":"ara-mcp-t10-missing-sequence"}`),
	})
	if err != nil || !missing.IsError {
		t.Fatalf("missing-sequence read = %#v, err=%v; want MCP tool error", missing, err)
	}
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
	if restartHost != "" {
		_ = clientSession.Close()
		clientSession = nil
		restartCtx, cancelRestart := context.WithTimeout(t.Context(), 20*time.Second)
		restart := exec.CommandContext(restartCtx, "ssh", "-o", "BatchMode=yes", restartHost, "sudo", "systemctl", "restart", "ara-mcp.service")
		output, err := restart.CombinedOutput()
		cancelRestart()
		if err != nil {
			clientSession, _ = connectLiveHTTP(t.Context(), liveMCPURL, liveMCPToken)
			t.Fatalf("restart live systemd adapter: %v: %s", err, output)
		}
		controlID = ""
		reconnectCtx, cancelReconnect := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancelReconnect()
		for {
			clientSession, err = connectLiveHTTP(reconnectCtx, liveMCPURL, liveMCPToken)
			if err == nil {
				break
			}
			select {
			case <-reconnectCtx.Done():
				t.Fatalf("reconnect to systemd service: %v", err)
			case <-time.After(250 * time.Millisecond):
			}
		}
		begin = liveCallTool(t, clientSession, "begin_control", `{}`)
		controlID = liveString(begin, "control_id")
		continued := liveCallTool(t, clientSession, "get_sequence_state", `{"sequence_id":"`+sequenceID+`"}`)
		continuedState, continuedRunID := liveRunIdentity(continued)
		if controlID == "" || !activeRunState(continuedState) || continuedRunID != runID {
			t.Fatalf("Ara run after systemd restart = %q/%q, control ID present=%t; want active run %q and fresh control", continuedState, continuedRunID, controlID != "", runID)
		}
	}
	if stdioCommand != nil {
		controlID = ""
		if err := clientSession.Close(); err != nil {
			t.Fatalf("close stdio client during active simulator run: %v", err)
		}
		clientSession = nil
		if err := stdioCommand.Wait(); err != nil {
			t.Fatalf("stop stdio adapter during active simulator run: %v; stderr=%s", err, stdioStderr.String())
		}
		stdioCommand = nil
		sessionInfo, _, err := client.GetServerSessionWithRequestID(t.Context(), "live-t10-after-adapter-close")
		if err != nil {
			t.Fatal(err)
		}
		if sessionInfo.Connected {
			t.Fatal("Ara still reports the adapter control session after stdio shutdown")
		}
		stdioCommand, stdioStderr, clientSession = startLiveStdioSession(t, baseURL)
		begin = liveCallTool(t, clientSession, "begin_control", `{}`)
		controlID = liveString(begin, "control_id")
		if controlID == "" {
			t.Fatalf("reconnected begin_control omitted control_id: %#v", begin)
		}
		stateResult := liveCallTool(t, clientSession, "get_sequence_state", `{"sequence_id":"`+sequenceID+`"}`)
		continuedState, continuedRunID := liveRunIdentity(stateResult)
		if !activeRunState(continuedState) || continuedRunID != runID {
			t.Fatalf("Ara run after stdio adapter restart = %q/%q, want active run %q", continuedState, continuedRunID, runID)
		}
	}
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

	capture := liveCallTool(t, clientSession, "capture_exposure", `{"control_id":"`+controlID+`","intent_id":"ara-mcp-t10-omnisim-exposure","exposure_sec":0.1,"bin_x":1,"bin_y":1}`)
	var captureResult EquipmentActionResult
	decodeLiveContent(t, capture, &captureResult)
	if captureResult.Mutation.Outcome != ara.OutcomeAccepted || captureResult.Frame.FrameID == "" {
		t.Fatalf("capture_exposure result = %#v; want accepted simulator frame", captureResult)
	}
	previewCtx, cancelPreview := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancelPreview()
	for {
		preview, err := clientSession.CallTool(previewCtx, &mcp.CallToolParams{
			Name: "get_frame_preview", Arguments: json.RawMessage(`{"frame_id":"` + captureResult.Frame.FrameID + `"}`),
		})
		if err == nil && !preview.IsError {
			var image *mcp.ImageContent
			for _, content := range preview.Content {
				if candidate, ok := content.(*mcp.ImageContent); ok {
					image = candidate
					break
				}
			}
			if image == nil || image.MIMEType != "image/jpeg" || len(image.Data) < 2 || len(image.Data) > 1<<20 || image.Data[0] != 0xff || image.Data[1] != 0xd8 {
				t.Fatalf("get_frame_preview content = %#v; want bounded JPEG image", preview.Content)
			}
			t.Logf("live simulator JPEG preview size = %d bytes", len(image.Data))
			break
		}
		select {
		case <-previewCtx.Done():
			t.Fatalf("simulated frame preview did not become available: %v", previewCtx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
}

type liveBearerTransport struct{ token string }

func connectLiveHTTP(ctx context.Context, endpoint, token string) (*mcp.ClientSession, error) {
	return mcp.NewClient(&mcp.Implementation{Name: "t10-live-http-check", Version: "1"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: &http.Client{Transport: liveBearerTransport{token: token}},
		}, nil)
}

func startLiveStdioSession(t *testing.T, baseURL string) (*exec.Cmd, *bytes.Buffer, *mcp.ClientSession) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "run", "../../cmd/ara-mcp", "--ara-url", baseURL, "serve")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := new(bytes.Buffer)
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t10-live-stdio-check", Version: "1"}, nil).
		Connect(t.Context(), &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("connect live stdio client: %v; stderr=%s", err, stderr.String())
	}
	return command, stderr, session
}

func (transport liveBearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(request)
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
