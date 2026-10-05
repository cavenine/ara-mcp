// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerSequenceExecutionTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "get_sequence_state",
		Description: "Read Ara's current state for a saved sequence. Missing in-memory run state is not evidence of completion.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"sequence_id": stringSchema("Ara sequence UUID"),
		}, "sequence_id"),
		OutputSchema: &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{}},
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SequenceInput) (jsontext.Value, error) {
		return getAndObserveSequenceState(ctx, instrumentation, client, input.SequenceID)
	})
	if control == nil {
		return
	}
	startSchema := executionMutationSchema()
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "start_sequence",
		Description: "Check the saved sequence, profile, connected devices, and live camera/filter limits, then start it through Ara. ContinueOnError plans are refused because Ara may report them completed after instruction failures. Start is asynchronous; the result reports acceptance and one immediate state observation, not completion.",
		InputSchema: executionMutationInputSchema(false), OutputSchema: startSchema,
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input sequenceStartInput) (SequenceExecutionResult, error) {
		return startSequence(ctx, instrumentation, client, control, input)
	})
	for _, action := range []string{"pause", "resume", "stop", "abort"} {
		action := action
		addTool(server, instrumentation, &mcp.Tool{
			Name:        action + "_sequence",
			Description: actionDescription(action),
			InputSchema: executionMutationInputSchema(true), OutputSchema: startSchema,
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input sequenceRunControlInput) (SequenceExecutionResult, error) {
			return controlSequence(ctx, instrumentation, client, control, action, input)
		})
	}
}

type sequenceStartInput struct {
	ControlID  string `json:"control_id"`
	IntentID   string `json:"intent_id"`
	SequenceID string `json:"sequence_id"`
}

type sequenceRunControlInput struct {
	ControlID     string `json:"control_id"`
	IntentID      string `json:"intent_id"`
	SequenceID    string `json:"sequence_id"`
	ExpectedRunID string `json:"expected_run_id"`
}

// SequenceExecutionResult separates Ara command acceptance from its observed state.
type SequenceExecutionResult struct {
	Mutation              MutationReceipt `json:"mutation"`
	State                 jsontext.Value  `json:"state,omitempty"`
	StateObservationError string          `json:"state_observation_error,omitempty"`
}

type sequenceRun struct {
	SequenceID string `json:"sequence_id"`
	RunID      string `json:"run_id"`
	State      string `json:"state"`
}

func executionMutationInputSchema(runControl bool) *jsonschema.Schema {
	properties := map[string]*jsonschema.Schema{
		"control_id":  stringSchema("Current control ID from begin_control"),
		"intent_id":   stringSchema("Stable ID for this logical execution command"),
		"sequence_id": stringSchema("Ara sequence UUID"),
	}
	required := []string{"control_id", "intent_id", "sequence_id"}
	if runControl {
		properties["expected_run_id"] = stringSchema("Run ID returned by Ara sequence state; prevents stale run control")
		required = append(required, "expected_run_id")
	}
	return objectSchema(properties, required...)
}

func executionMutationSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"mutation":                sequenceMutationSchema().Properties["mutation"],
		"state":                   {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
		"state_observation_error": stringSchema("Bounded error class if the immediate state read was unavailable"),
	}, "mutation")
}

func actionDescription(action string) string {
	switch action {
	case "pause":
		return "Ask Ara to pause the specified active run, then return its accepted receipt and an immediate state observation."
	case "resume":
		return "Ask Ara to resume the specified active run with recentering and refocusing disabled, then return its accepted receipt and an immediate state observation."
	case "stop":
		return "Ask Ara to stop the specified active run, then return its accepted receipt and an immediate state observation."
	default:
		return "Ask Ara to abort the specified active run, then return its accepted receipt and an immediate state observation."
	}
}

func startSequence(ctx context.Context, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager, input sequenceStartInput) (SequenceExecutionResult, error) {
	sequenceID, err := normalizeSequenceID(input.SequenceID)
	if err != nil {
		return SequenceExecutionResult{}, fmt.Errorf("%w: sequence ID must be a UUID", errInvalidArgument)
	}
	arguments, err := json.Marshal(struct {
		SequenceID string `json:"sequence_id"`
	}{sequenceID})
	if err != nil {
		return SequenceExecutionResult{}, fmt.Errorf("encode start intent: %w", err)
	}
	receipt, err := control.DispatchMutation(ctx, MutationRequest{
		ControlID: input.ControlID, IntentID: input.IntentID, Operation: "start_sequence",
		Arguments: arguments, Kind: MutationStartRun,
		Preflight: func(ctx context.Context) (RunSnapshot, error) {
			if err := preflightSequence(ctx, client, sequenceID); err != nil {
				return RunSnapshot{}, err
			}
			return currentRunSnapshot(ctx, client), nil
		},
	}, func(ctx context.Context) (MutationReceipt, error) {
		result, dispatchErr := client.StartSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
		mutation := MutationReceipt{Outcome: result.Outcome, ReceiptID: result.ReceiptID, SequenceID: sequenceID, RetrySafe: false}
		if dispatchErr == nil && result.Outcome != ara.OutcomeAccepted {
			mutation.Outcome, mutation.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
			dispatchErr = errors.New("Ara sequence start returned a non-accepted response")
		}
		if dispatchErr != nil {
			mutation.ErrorClass = toolErrorClass(dispatchErr)
		}
		return mutation, dispatchErr
	})
	if err != nil {
		if receipt.Outcome == ara.OutcomeUnknown {
			return executionResult(ctx, instrumentation, client, "start", sequenceID, receipt), nil
		}
		return SequenceExecutionResult{}, sequenceMutationError("start sequence", receipt, err)
	}
	return executionResult(ctx, instrumentation, client, "start", sequenceID, receipt), nil
}

func controlSequence(ctx context.Context, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager, action string, input sequenceRunControlInput) (SequenceExecutionResult, error) {
	sequenceID, err := normalizeSequenceID(input.SequenceID)
	if err != nil {
		return SequenceExecutionResult{}, fmt.Errorf("%w: sequence ID must be a UUID", errInvalidArgument)
	}
	expectedRunID, err := normalizeSequenceID(input.ExpectedRunID)
	if err != nil {
		return SequenceExecutionResult{}, fmt.Errorf("%w: expected_run_id must be an Ara run UUID", errInvalidArgument)
	}
	arguments, err := json.Marshal(struct {
		SequenceID string `json:"sequence_id"`
	}{sequenceID})
	if err != nil {
		return SequenceExecutionResult{}, fmt.Errorf("encode run-control intent: %w", err)
	}
	kind := MutationRunControl
	if action == "stop" || action == "abort" {
		kind = MutationInterrupt
	}
	receipt, err := control.DispatchMutation(ctx, MutationRequest{
		ControlID: input.ControlID, IntentID: input.IntentID, Operation: action + "_sequence",
		Arguments: arguments, Kind: kind, ExpectedRunID: expectedRunID,
		Preflight: func(ctx context.Context) (RunSnapshot, error) {
			state, _, err := client.GetSequenceStateWithRequestID(ctx, sequenceID, requestID(ctx))
			if err != nil {
				return RunSnapshot{}, err
			}
			run, err := decodeSequenceRun(state)
			if err != nil {
				return RunSnapshot{}, err
			}
			if run.SequenceID != sequenceID {
				return RunSnapshot{}, errors.New("Ara sequence state returned a different sequence ID")
			}
			return RunSnapshot{Known: true, ActiveRun: activeRunState(run.State), RunID: run.RunID}, nil
		},
	}, func(ctx context.Context) (MutationReceipt, error) {
		var result ara.Result
		var dispatchErr error
		switch action {
		case "pause":
			result, dispatchErr = client.PauseSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
		case "resume":
			result, dispatchErr = client.ResumeSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
		case "stop":
			result, dispatchErr = client.StopSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
		case "abort":
			result, dispatchErr = client.AbortSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
		}
		mutation := MutationReceipt{Outcome: result.Outcome, ReceiptID: result.ReceiptID, SequenceID: sequenceID}
		if dispatchErr == nil && result.Outcome != ara.OutcomeAccepted {
			mutation.Outcome, mutation.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
			dispatchErr = errors.New("Ara run control returned a non-accepted response")
		}
		if dispatchErr != nil {
			mutation.ErrorClass = toolErrorClass(dispatchErr)
		}
		return mutation, dispatchErr
	})
	if err != nil {
		if receipt.Outcome == ara.OutcomeUnknown {
			return executionResult(ctx, instrumentation, client, action, sequenceID, receipt), nil
		}
		return SequenceExecutionResult{}, sequenceMutationError(action+" sequence", receipt, err)
	}
	return executionResult(ctx, instrumentation, client, action, sequenceID, receipt), nil
}

func executionResult(ctx context.Context, instrumentation toolInstrumentation, client *ara.Client, action, sequenceID string, receipt MutationReceipt) SequenceExecutionResult {
	result := SequenceExecutionResult{Mutation: receipt}
	state, err := getAndObserveSequenceState(ctx, instrumentation, client, sequenceID)
	if err != nil {
		result.StateObservationError = toolErrorClass(err)
	} else {
		result.State = state
	}
	fields := []any{"service", "ara-mcp", "version", instrumentation.version, "component", "sequence", "event", "sequence_run_command",
		"transport", instrumentation.transport, "request_id", requestID(ctx), "sequence_id", sequenceID,
		"outcome", receipt.Outcome, "action", action}
	if receipt.ReceiptID != "" {
		fields = append(fields, "operation_id", receipt.ReceiptID)
	}
	if result.StateObservationError != "" {
		fields = append(fields, "state_observation_error", result.StateObservationError)
	}
	level, message := slog.LevelInfo, "Ara sequence command accepted"
	if receipt.Outcome == ara.OutcomeUnknown {
		level, message = slog.LevelWarn, "Ara sequence command outcome unknown"
	}
	instrumentation.logger.Log(ctx, level, message, fields...)
	return result
}

func preflightSequence(ctx context.Context, client *ara.Client, sequenceID string) error {
	detail, _, err := client.GetSequenceWithRequestID(ctx, sequenceID, requestID(ctx))
	if err != nil {
		return fmt.Errorf("read saved sequence before start: %w", err)
	}
	var saved struct {
		ID   string         `json:"id"`
		Body jsontext.Value `json:"body"`
	}
	if err := json.Unmarshal(detail, &saved); err != nil {
		return fmt.Errorf("decode saved sequence before start: %w", err)
	}
	if canonical, err := normalizeSequenceID(saved.ID); err != nil || canonical != sequenceID {
		return errors.New("Ara saved sequence response omitted or changed its ID")
	}
	if !validSequenceBody(saved.Body) {
		return fmt.Errorf("%w: saved sequence body is unavailable", errInvalidArgument)
	}
	if err := validateSupportedSequenceBody(saved.Body); err != nil {
		return fmt.Errorf("%w: sequence is outside the executable palette: %w", errInvalidArgument, err)
	}
	validation, _, err := client.ValidateSequenceWithRequestID(ctx, saved.Body, requestID(ctx))
	if err != nil {
		return fmt.Errorf("validate saved sequence before start: %w", err)
	}
	if !validation.Valid {
		return fmt.Errorf("%w: Ara sequence validation failed: %s", errInvalidArgument, boundedSequenceText(validation.Reason))
	}
	var profile struct {
		CurrentProfileID *string `json:"current_profile_id"`
	}
	state, _, err := client.GetServerStateWithRequestID(ctx, requestID(ctx))
	if err != nil {
		return fmt.Errorf("read active Ara profile before start: %w", err)
	}
	if err := json.Unmarshal(state, &profile); err != nil {
		return fmt.Errorf("decode active Ara profile before start: %w", err)
	}
	profileID, err := selectedProfileID(ctx, client, profile.CurrentProfileID, requestID(ctx))
	if err != nil {
		return err
	}
	if profileID == "" {
		return fmt.Errorf("%w: start requires an active Ara profile", errInvalidArgument)
	}
	requirements, err := sequenceRequirements(saved.Body)
	if err != nil {
		return fmt.Errorf("inspect sequence requirements: %w", err)
	}
	cameraBody, err := readConnectedDevice(ctx, client, ara.DeviceCamera)
	if err != nil {
		return err
	}
	var camera struct {
		Capabilities *cameraCapabilities `json:"capabilities"`
	}
	if err := json.Unmarshal(cameraBody, &camera); err != nil || camera.Capabilities == nil {
		return fmt.Errorf("%w: Ara camera capabilities are unavailable", errRunStateUnknown)
	}
	if err := validateCameraExecutionRequirements(*camera.Capabilities, requirements.Exposures); err != nil {
		return err
	}
	if len(requirements.Filters) > 0 {
		filterWheelBody, err := readConnectedDevice(ctx, client, ara.DeviceFilterWheel)
		if err != nil {
			return err
		}
		var wheel struct {
			Slots []struct {
				Position int    `json:"position"`
				Name     string `json:"name"`
			} `json:"slots"`
		}
		if err := json.Unmarshal(filterWheelBody, &wheel); err != nil || len(wheel.Slots) == 0 {
			return fmt.Errorf("%w: Ara filter-wheel slot capabilities are unavailable", errRunStateUnknown)
		}
		labelsBody, _, err := client.GetProfileFilterWheelLabelsWithRequestID(ctx, requestID(ctx))
		if err != nil {
			return fmt.Errorf("read Ara filter slot labels before start: %w", err)
		}
		var labels struct {
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal(labelsBody, &labels); err != nil || len(labels.Labels) == 0 {
			return fmt.Errorf("%w: Ara filter slot labels are unavailable", errRunStateUnknown)
		}
		for _, filter := range requirements.Filters {
			if filter.Position >= len(labels.Labels) || filter.Position >= len(wheel.Slots) ||
				wheel.Slots[filter.Position].Position != filter.Position {
				return fmt.Errorf("%w: sequence filter slot %d is not available on the connected wheel", errInvalidArgument, filter.Position)
			}
			if filter.Position >= len(labels.Labels) || strings.TrimSpace(labels.Labels[filter.Position]) == "" ||
				!strings.EqualFold(strings.TrimSpace(labels.Labels[filter.Position]), filter.Name) {
				return fmt.Errorf("%w: sequence filter %q at slot %d does not match the active Ara filter-wheel labels", errInvalidArgument, filter.Name, filter.Position)
			}
		}
	}
	return nil
}

func readConnectedDevice(ctx context.Context, client *ara.Client, device ara.DeviceType) (jsontext.Value, error) {
	body, _, err := client.GetDeviceStatusWithRequestID(ctx, device, requestID(ctx))
	if err != nil {
		return nil, fmt.Errorf("read Ara %s status before start: %w", device, err)
	}
	var status struct {
		Connected *bool  `json:"connected"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("%w: Ara %s connection state is unknown", errRunStateUnknown, device)
	}
	connected := status.Connected != nil && *status.Connected
	known := status.Connected != nil
	if status.Connected == nil && status.State != "" {
		connected = strings.EqualFold(status.State, "connected")
		known = true
	}
	if !known {
		return nil, fmt.Errorf("%w: Ara %s connection state is unknown", errRunStateUnknown, device)
	}
	if !connected {
		return nil, fmt.Errorf("%w: Ara %s is not connected", errInvalidArgument, device)
	}
	return body, nil
}

type sequenceFilterRequirement struct {
	Name     string
	Position int
}

type sequenceExposureRequirement struct {
	Exposure float64
	Gain     *int
	Offset   *int
	BinX     int
	BinY     int
}

type sequenceExecutionRequirements struct {
	Exposures []sequenceExposureRequirement
	Filters   []sequenceFilterRequirement
}

func sequenceRequirements(body jsontext.Value) (sequenceExecutionRequirements, error) {
	root, err := sequenceObject(body)
	if err != nil {
		return sequenceExecutionRequirements{}, err
	}
	var requirements sequenceExecutionRequirements
	var visit func(map[string]jsontext.Value, int) error
	visit = func(container map[string]jsontext.Value, depth int) error {
		if depth > maxSequenceNestingDepth {
			return fmt.Errorf("sequence container nesting exceeds %d levels", maxSequenceNestingDepth)
		}
		items, err := sequenceCollection(container["Items"], false)
		if err != nil {
			return err
		}
		for _, raw := range items {
			item, err := sequenceObject(raw)
			if err != nil {
				return err
			}
			typeName, _ := sequenceString(item["$type"])
			switch typeName {
			case sequentialContainerType:
				if err := visit(item, depth+1); err != nil {
					return err
				}
			case switchFilterType:
				filter, err := sequenceObject(item["Filter"])
				if err != nil {
					return err
				}
				name, _ := sequenceString(filter["_name"])
				position, _ := sequenceInt(filter["_position"])
				requirements.Filters = append(requirements.Filters, sequenceFilterRequirement{Name: strings.TrimSpace(name), Position: position})
			case takeExposureType:
				exposure, _ := sequenceFloat(item["ExposureTime"])
				requirement := sequenceExposureRequirement{Exposure: exposure, BinX: 1, BinY: 1}
				if raw, ok := item["Gain"]; ok {
					value, _ := sequenceInt(raw)
					requirement.Gain = &value
				}
				if raw, ok := item["Offset"]; ok {
					value, _ := sequenceInt(raw)
					requirement.Offset = &value
				}
				if raw, ok := item["Binning"]; ok && raw.Kind() != 'n' {
					binning, err := sequenceObject(raw)
					if err != nil {
						return err
					}
					requirement.BinX, _ = sequenceInt(binning["X"])
					requirement.BinY, _ = sequenceInt(binning["Y"])
				}
				requirements.Exposures = append(requirements.Exposures, requirement)
			}
		}
		return nil
	}
	if err := visit(root, 1); err != nil {
		return sequenceExecutionRequirements{}, err
	}
	return requirements, nil
}

type cameraCapabilities struct {
	MinExposureSec *float64 `json:"min_exposure_sec"`
	MaxExposureSec *float64 `json:"max_exposure_sec"`
	MinGain        *int     `json:"min_gain"`
	MaxGain        *int     `json:"max_gain"`
	MinOffset      *int     `json:"min_offset"`
	MaxOffset      *int     `json:"max_offset"`
	MinBinX        *int     `json:"min_bin_x"`
	MaxBinX        *int     `json:"max_bin_x"`
	MinBinY        *int     `json:"min_bin_y"`
	MaxBinY        *int     `json:"max_bin_y"`
}

func validateCameraExecutionRequirements(capabilities cameraCapabilities, exposures []sequenceExposureRequirement) error {
	if capabilities.MinExposureSec == nil || capabilities.MaxExposureSec == nil ||
		math.IsNaN(*capabilities.MinExposureSec) || math.IsInf(*capabilities.MinExposureSec, 0) ||
		math.IsNaN(*capabilities.MaxExposureSec) || math.IsInf(*capabilities.MaxExposureSec, 0) ||
		*capabilities.MinExposureSec < 0 || *capabilities.MaxExposureSec < *capabilities.MinExposureSec {
		return fmt.Errorf("%w: Ara camera exposure limits are unavailable or invalid", errRunStateUnknown)
	}
	for _, exposure := range exposures {
		if exposure.Exposure < *capabilities.MinExposureSec || exposure.Exposure > *capabilities.MaxExposureSec {
			return fmt.Errorf("%w: exposure %.6g seconds is outside Ara camera limits %.6g–%.6g seconds", errInvalidArgument,
				exposure.Exposure, *capabilities.MinExposureSec, *capabilities.MaxExposureSec)
		}
		if err := checkCameraIntegerLimit("gain", exposure.Gain, capabilities.MinGain, capabilities.MaxGain); err != nil {
			return err
		}
		if err := checkCameraIntegerLimit("offset", exposure.Offset, capabilities.MinOffset, capabilities.MaxOffset); err != nil {
			return err
		}
		for _, axis := range []struct {
			name  string
			value int
			min   *int
			max   *int
		}{{"binning X", exposure.BinX, capabilities.MinBinX, capabilities.MaxBinX}, {"binning Y", exposure.BinY, capabilities.MinBinY, capabilities.MaxBinY}} {
			if axis.min == nil || axis.max == nil || *axis.min < 1 || *axis.max < *axis.min {
				return fmt.Errorf("%w: Ara camera %s limits are unavailable or invalid", errRunStateUnknown, axis.name)
			}
			if axis.value < *axis.min || axis.value > *axis.max {
				return fmt.Errorf("%w: %s %d is outside Ara camera limits %d–%d", errInvalidArgument, axis.name, axis.value, *axis.min, *axis.max)
			}
		}
	}
	return nil
}

func checkCameraIntegerLimit(name string, value, minimum, maximum *int) error {
	if value == nil || *value == -1 {
		return nil // -1 and omission use Ara/camera defaults.
	}
	if minimum == nil || maximum == nil || *maximum < *minimum {
		return fmt.Errorf("%w: Ara camera %s limits are unavailable or invalid", errRunStateUnknown, name)
	}
	if *value < *minimum || *value > *maximum {
		return fmt.Errorf("%w: %s %d is outside Ara camera limits %d–%d", errInvalidArgument, name, *value, *minimum, *maximum)
	}
	return nil
}

func currentRunSnapshot(ctx context.Context, client *ara.Client) RunSnapshot {
	page, _, err := client.ListSequencesWithRequestID(ctx, 100, requestID(ctx))
	if err != nil || page.HasMore || page.NextCursor != nil || len(page.Items) == 100 {
		// ponytail: Ara's list cannot prove completeness at its 100-item cap; keep
		// starts fail-closed there until upstream provides cursor-backed run discovery.
		return RunSnapshot{}
	}
	snapshot := RunSnapshot{Known: true}
	for _, item := range page.Items {
		var saved struct {
			CurrentRunState *string `json:"current_run_state"`
		}
		if err := json.Unmarshal(item, &saved); err != nil {
			return RunSnapshot{}
		}
		if saved.CurrentRunState == nil {
			continue
		}
		if !knownRunState(*saved.CurrentRunState) {
			return RunSnapshot{}
		}
		if activeRunState(*saved.CurrentRunState) {
			snapshot.ActiveRun = true
		}
	}
	return snapshot
}

func decodeSequenceRun(body jsontext.Value) (sequenceRun, error) {
	var run sequenceRun
	if err := json.Unmarshal(body, &run); err != nil {
		return sequenceRun{}, fmt.Errorf("decode Ara sequence state: %w", err)
	}
	if run.SequenceID == "" || run.RunID == "" || run.State == "" || !knownRunState(run.State) {
		return sequenceRun{}, errors.New("Ara sequence state omitted a known sequence, run, or state value")
	}
	return run, nil
}

func knownRunState(state string) bool {
	switch strings.ToLower(state) {
	case "idle", "starting", "running", "paused", "aborting", "stopped", "completed", "failed", "pausedawaitinguser":
		return true
	default:
		return false
	}
}

func activeRunState(state string) bool {
	switch strings.ToLower(state) {
	case "starting", "running", "paused", "aborting", "pausedawaitinguser":
		return true
	default:
		return false
	}
}

func getSequenceState(ctx context.Context, client *ara.Client, sequenceID string) (jsontext.Value, error) {
	sequenceID, err := normalizeSequenceID(sequenceID)
	if err != nil {
		return nil, fmt.Errorf("%w: sequence ID must be a UUID", errInvalidArgument)
	}
	state, _, err := client.GetSequenceStateWithRequestID(ctx, sequenceID, requestID(ctx))
	if err != nil {
		return nil, fmt.Errorf("read Ara sequence state: %w", err)
	}
	var identity struct {
		SequenceID string `json:"sequence_id"`
	}
	if err := json.Unmarshal(state, &identity); err != nil || identity.SequenceID != sequenceID {
		return nil, errors.New("Ara sequence state omitted or changed its sequence ID")
	}
	return state, nil
}

func getAndObserveSequenceState(ctx context.Context, instrumentation toolInstrumentation, client *ara.Client, sequenceID string) (jsontext.Value, error) {
	state, err := getSequenceState(ctx, client, sequenceID)
	if err != nil {
		return nil, err
	}
	instrumentation.observations.observe(ctx, state)
	return state, nil
}
