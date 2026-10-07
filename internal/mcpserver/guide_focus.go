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

type guideFocusInput struct {
	ControlID   string  `json:"control_id"`
	IntentID    string  `json:"intent_id"`
	ExposureSec float64 `json:"exposure_sec,omitempty"`
	Binning     *int    `json:"binning,omitempty"`
}

type guideFocusStopInput struct {
	ControlID string `json:"control_id"`
	IntentID  string `json:"intent_id"`
}

type guideFocusMutationResult struct {
	Mutation         MutationReceipt       `json:"mutation"`
	Status           *ara.GuideFocusStatus `json:"status,omitempty"`
	ObservationError string                `json:"observation_error,omitempty"`
}

func registerGuideFocusTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_guide_focus_status", Description: "Read Ara's guide-camera focus loop state; this read does not acquire or change the guider lease.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (ara.GuideFocusStatus, error) {
		status, _, err := client.GetGuideFocusStatusWithRequestID(ctx, requestID(ctx))
		return status, err
	})
	frameTool := &mcp.Tool{Name: "get_guide_focus_frame", Description: "Read Ara's latest guide-focus JPEG as MCP image content (maximum 1 MiB); unavailable until Ara has rendered a frame.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}}
	mcp.AddTool(server, frameTool, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		frame, result, err := client.GetGuideFocusFrameWithRequestID(ctx, requestID(ctx))
		if err != nil {
			return nil, nil, err
		}
		if result.Status == http.StatusNoContent {
			return nil, map[string]any{"available": false}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: frame, MIMEType: "image/jpeg"}}}, map[string]any{"available": true, "size_bytes": len(frame)}, nil
	})
	if control == nil {
		return
	}
	inputSchema := objectSchema(map[string]*jsonschema.Schema{
		"control_id":   stringSchema("Current control ID from begin_control"),
		"intent_id":    stringSchema("Stable ID for this logical action; uncertain starts must be reconciled, never resent with a new ID"),
		"exposure_sec": {Type: "number", Description: "Guide-camera exposure in seconds, Ara default 2; range 0.05–30"},
		"binning":      {Type: "integer", Description: "Optional guide-camera binning, at least 1"},
	}, "control_id", "intent_id")
	addTool(server, instrumentation, &mcp.Tool{
		Name: "start_guide_focus", Description: "Start Ara's guide-camera focus loop (exposure seconds; default 2, range 0.05–30). Requires control. Ara returns acceptance (202), not completed focus. Refuses an active imaging run, disconnected guider, guiding, or polar alignment.",
		InputSchema: inputSchema, Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input guideFocusInput) (guideFocusMutationResult, error) {
		if input.ExposureSec == 0 {
			input.ExposureSec = 2
		}
		if !finite(input.ExposureSec) || input.ExposureSec < 0.05 || input.ExposureSec > 30 || input.Binning != nil && *input.Binning < 1 {
			return guideFocusMutationResult{}, fmt.Errorf("%w: exposure must be 0.05–30 seconds and binning at least 1", errInvalidArgument)
		}
		arguments, err := json.Marshal(struct {
			Exposure float64 `json:"exposure_sec"`
			Binning  *int    `json:"binning,omitempty"`
		}{input.ExposureSec, input.Binning})
		if err != nil {
			return guideFocusMutationResult{}, err
		}
		receipt, err := control.DispatchMutation(ctx, MutationRequest{ControlID: input.ControlID, IntentID: input.IntentID, Operation: "start_guide_focus", Arguments: arguments, Kind: MutationManual, Preflight: func(ctx context.Context) (RunSnapshot, error) {
			if _, err := readConnectedDevice(ctx, client, ara.DeviceGuider); err != nil {
				return RunSnapshot{}, fmt.Errorf("guide focus requires a connected guider: %w", err)
			}
			pa, _, err := client.GetPolarAlignStatusForGuideFocus(ctx, requestID(ctx))
			if err != nil {
				return RunSnapshot{}, fmt.Errorf("read polar-alignment lease state: %w", err)
			}
			if pa.Active || pa.State == "seeding" || pa.State == "adjusting" || pa.State == "paused" {
				return RunSnapshot{}, fmt.Errorf("%w: polar alignment is using the guider", errEquipmentUnavailable)
			}
			return currentRunSnapshot(ctx, client), nil
		}}, func(ctx context.Context) (MutationReceipt, error) {
			result, dispatchErr := client.StartGuideFocusWithRequestID(ctx, ara.GuideFocusStartRequest{ExposureSec: input.ExposureSec, Binning: input.Binning}, requestID(ctx))
			r := araMutationReceipt(result, dispatchErr)
			if dispatchErr != nil {
				r.ErrorClass = toolErrorClass(dispatchErr)
			}
			if dispatchErr == nil && result.Outcome != ara.OutcomeAccepted {
				dispatchErr = errors.New("Ara guide-focus start did not return 202 acceptance")
				r.Outcome, r.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
			}
			return r, dispatchErr
		})
		if err != nil && receipt.Outcome != ara.OutcomeUnknown {
			return guideFocusMutationResult{}, fmt.Errorf("start Ara guide focus: %w", err)
		}
		output := guideFocusMutationResult{Mutation: receipt}
		if err == nil && receipt.Outcome == ara.OutcomeAccepted && !receipt.Replayed {
			status, _, statusErr := client.GetGuideFocusStatusWithRequestID(ctx, requestID(ctx))
			if statusErr != nil {
				output.ObservationError = toolErrorClass(statusErr)
			} else {
				output.Status = &status
			}
		}
		return output, nil
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "stop_guide_focus", Description: "Stop Ara's guide-camera focus loop. Ara's synchronous 204 waits for the in-flight guider frame to drain; the result includes an immediate status observation when available.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"control_id": stringSchema("Current control ID from begin_control"),
			"intent_id":  stringSchema("Stable ID for this logical action"),
		}, "control_id", "intent_id"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input guideFocusStopInput) (guideFocusMutationResult, error) {
		receipt, err := control.DispatchMutation(ctx, MutationRequest{ControlID: input.ControlID, IntentID: input.IntentID, Operation: "stop_guide_focus", Arguments: []byte(`{}`), Kind: MutationInterrupt}, func(ctx context.Context) (MutationReceipt, error) {
			result, dispatchErr := client.StopGuideFocusWithRequestID(ctx, requestID(ctx))
			r := araMutationReceipt(result, dispatchErr)
			if dispatchErr != nil {
				r.ErrorClass = toolErrorClass(dispatchErr)
			}
			if dispatchErr == nil && result.Outcome != ara.OutcomeCompleted {
				dispatchErr = errors.New("Ara guide-focus stop did not return 204 drain completion")
				r.Outcome, r.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
			}
			return r, dispatchErr
		})
		if err != nil && receipt.Outcome != ara.OutcomeUnknown {
			return guideFocusMutationResult{}, fmt.Errorf("stop Ara guide focus: %w", err)
		}
		output := guideFocusMutationResult{Mutation: receipt}
		if err == nil && receipt.Outcome == ara.OutcomeCompleted && !receipt.Replayed {
			status, _, statusErr := client.GetGuideFocusStatusWithRequestID(ctx, requestID(ctx))
			if statusErr != nil {
				output.ObservationError = toolErrorClass(statusErr)
			} else {
				output.Status = &status
			}
		}
		return output, nil
	})
}
