// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type AutofocusCalibrationResult struct {
	Calibrated  bool                      `json:"calibrated"`
	Calibration *ara.AutofocusCalibration `json:"calibration,omitempty"`
}

type AutofocusMutationInput struct {
	ControlID string `json:"control_id"`
	IntentID  string `json:"intent_id"`
}

type AutofocusMutationResult struct {
	Mutation              MutationReceipt             `json:"mutation"`
	State                 *ara.AutofocusRun           `json:"state,omitempty"`
	StateObservationError string                      `json:"state_observation_error,omitempty"`
	Calibration           *AutofocusCalibrationResult `json:"calibration,omitempty"`
	CalibrationReadError  string                      `json:"calibration_read_error,omitempty"`
}

func registerAutofocusTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_autofocus_state", Description: "Read Ara's current or most recent autofocus run, including probes, fit, outcome, and frame sequence.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (ara.AutofocusRun, error) {
		run, _, err := client.GetAutofocusStateWithRequestID(ctx, requestID(ctx))
		return run, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_autofocus_calibration", Description: "Read Ara's stored Smart Focus calibration; an uncalibrated rig returns calibrated=false.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (AutofocusCalibrationResult, error) {
		calibration, _, err := client.GetAutofocusCalibrationWithRequestID(ctx, requestID(ctx))
		if apiError, ok := errors.AsType[*ara.APIError](err); ok && apiError.Status == http.StatusNotFound {
			return AutofocusCalibrationResult{Calibrated: false}, nil
		}
		if err != nil {
			return AutofocusCalibrationResult{}, err
		}
		return AutofocusCalibrationResult{Calibrated: true, Calibration: &calibration}, nil
	})
	registerImageTool(server, instrumentation, &mcp.Tool{
		Name: "get_autofocus_frame", Description: "Read Ara's latest autofocus probe or confirmation JPEG as MCP image content (maximum 1 MiB). Metadata includes Ara's X-Frame-Seq; unavailable until a frame is rendered.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ struct{}) ([]byte, any, error) {
		frame, frameSeq, _, err := client.GetAutofocusFrameWithRequestID(ctx, requestID(ctx))
		return frame, map[string]any{"available": len(frame) > 0, "frame_seq": frameSeq, "mime_type": "image/jpeg", "size_bytes": len(frame)}, err
	})
	if control != nil {
		registerAutofocusMutationTools(server, instrumentation, client, control)
	}
}

func registerAutofocusMutationTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	input := objectSchema(map[string]*jsonschema.Schema{
		"control_id": stringSchema("Current control ID from begin_control"),
		"intent_id":  stringSchema("Stable ID for this logical action; uncertain actions are never redispatched"),
	}, "control_id", "intent_id")
	output := objectSchema(map[string]*jsonschema.Schema{
		"mutation":                sequenceMutationSchema().Properties["mutation"],
		"state":                   {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
		"state_observation_error": stringSchema("Bounded error class if the immediate state read was unavailable"),
		"calibration":             {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
		"calibration_read_error":  stringSchema("Bounded error class if calibration reconciliation was unavailable"),
	}, "mutation")
	addTool(server, instrumentation, &mcp.Tool{
		Name: "cancel_autofocus", Description: "Request Ara to cancel the current autofocus run, including one started by a sequence. Requires control. Acceptance is not proof the focuser has stopped; inspect state/events.",
		InputSchema: input, OutputSchema: output, Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input AutofocusMutationInput) (AutofocusMutationResult, error) {
		result, err := dispatchAutofocusMutation(ctx, client, control, input, "cancel_autofocus", MutationInterrupt, ara.OutcomeAccepted, nil, func(ctx context.Context) (ara.Result, error) {
			return client.CancelAutofocusWithRequestID(ctx, requestID(ctx))
		})
		if err != nil || result.Mutation.Replayed || result.Mutation.Outcome != ara.OutcomeAccepted && result.Mutation.Outcome != ara.OutcomeUnknown {
			return result, err
		}
		state, _, observationErr := client.GetAutofocusStateWithRequestID(ctx, requestID(ctx))
		if observationErr != nil {
			result.StateObservationError = toolErrorClass(observationErr)
		} else {
			result.State = &state
		}
		return result, nil
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "recalibrate_autofocus", Description: "Clear Ara's stored Smart Focus calibration; the next successful Classic sweep rebuilds it. Requires control and an idle rig; this tool does not move the focuser.",
		InputSchema: input, OutputSchema: output, Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input AutofocusMutationInput) (AutofocusMutationResult, error) {
		preflight := func(ctx context.Context) (RunSnapshot, error) {
			snapshot := currentRunSnapshot(ctx, client)
			if !snapshot.Known || snapshot.ActiveRun {
				return snapshot, nil
			}
			run, _, err := client.GetAutofocusStateWithRequestID(ctx, requestID(ctx))
			if err != nil {
				return RunSnapshot{}, fmt.Errorf("read Ara autofocus state before recalibration: %w", err)
			}
			switch run.State {
			case "idle", "complete", "failed", "cancelled":
				return snapshot, nil
			case "running":
				return RunSnapshot{Known: true, ActiveRun: true}, nil
			default:
				return RunSnapshot{}, fmt.Errorf("%w: Ara autofocus state %q is unknown", errRunStateUnknown, run.State)
			}
		}
		result, err := dispatchAutofocusMutation(ctx, client, control, input, "recalibrate_autofocus", MutationManual, ara.OutcomeCompleted, preflight, func(ctx context.Context) (ara.Result, error) {
			return client.RecalibrateAutofocusWithRequestID(ctx, requestID(ctx))
		})
		if err != nil || result.Mutation.Replayed || result.Mutation.Outcome != ara.OutcomeCompleted && result.Mutation.Outcome != ara.OutcomeUnknown {
			return result, err
		}
		calibration, _, observationErr := client.GetAutofocusCalibrationWithRequestID(ctx, requestID(ctx))
		if apiError, ok := errors.AsType[*ara.APIError](observationErr); ok && apiError.Status == http.StatusNotFound {
			result.Calibration = &AutofocusCalibrationResult{Calibrated: false}
		} else if observationErr != nil {
			result.CalibrationReadError = toolErrorClass(observationErr)
		} else {
			result.Calibration = &AutofocusCalibrationResult{Calibrated: true, Calibration: &calibration}
		}
		return result, nil
	})
}

func dispatchAutofocusMutation(ctx context.Context, client *ara.Client, control *ControlManager, input AutofocusMutationInput, operation string, kind MutationKind, expected ara.Outcome, preflight func(context.Context) (RunSnapshot, error), dispatch func(context.Context) (ara.Result, error)) (AutofocusMutationResult, error) {
	arguments, err := json.Marshal(struct{}{})
	if err != nil {
		return AutofocusMutationResult{}, fmt.Errorf("encode autofocus action: %w", err)
	}
	receipt, err := control.DispatchMutation(ctx, MutationRequest{
		ControlID: input.ControlID, IntentID: input.IntentID, Operation: operation,
		Arguments: arguments, Kind: kind, Preflight: preflight,
	}, func(ctx context.Context) (MutationReceipt, error) {
		result, callErr := dispatch(ctx)
		mutation := araMutationReceipt(result, callErr)
		if callErr == nil && result.Outcome != expected {
			callErr = errors.New("Ara autofocus action returned an unexpected outcome")
			mutation.Outcome, mutation.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
		}
		if callErr != nil {
			mutation.ErrorClass = toolErrorClass(callErr)
		}
		return mutation, callErr
	})
	if err != nil && receipt.Outcome != ara.OutcomeUnknown {
		return AutofocusMutationResult{}, fmt.Errorf("Ara %s failed: %w", operation, err)
	}
	result := AutofocusMutationResult{Mutation: receipt}
	if receipt.Replayed {
		result.StateObservationError = "replayed_receipt_only"
	}
	return result, nil
}
