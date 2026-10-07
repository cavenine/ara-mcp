// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"uuid"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type SolveFrameInput struct {
	FrameID          string   `json:"frame_id"`
	ApproxRAHours    *float64 `json:"approx_ra_hours,omitempty"`
	ApproxDecDegrees *float64 `json:"approx_dec_degrees,omitempty"`
}

type CenterInput struct {
	ControlID  string  `json:"control_id"`
	IntentID   string  `json:"intent_id"`
	RAHours    float64 `json:"ra_hours"`
	DecDegrees float64 `json:"dec_degrees"`
}

type CancelCenterInput struct {
	ControlID string `json:"control_id"`
	IntentID  string `json:"intent_id"`
	JobID     string `json:"job_id"`
}

type CenterResult struct {
	Mutation MutationReceipt `json:"mutation"`
	Job      ara.BatchJob    `json:"job,omitzero"`
}

type CenterCancelResult struct {
	Mutation         MutationReceipt `json:"mutation"`
	Job              *ara.Job        `json:"job,omitempty"`
	ObservationError string          `json:"observation_error,omitempty"`
}

// registerPlateSolveTools attaches T18 tools to an MCP server. Call from server wiring
// with its shared instrumentation, Ara client, and control manager.
func registerPlateSolveTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	addTool(server, instrumentation, &mcp.Tool{
		Name: "solve_frame", Description: "Plate-solve a catalogued Ara frame. Returns coordinates in RA hours and declination degrees; no FITS data or server path is returned.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"frame_id":           stringSchema("Ara catalogued frame ID"),
			"approx_ra_hours":    {Type: "number", Description: "Optional approximate right ascension hint in hours; used with approximate declination"},
			"approx_dec_degrees": {Type: "number", Description: "Optional approximate declination hint in degrees; used with approximate right ascension"},
		}, "frame_id"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SolveFrameInput) (ara.PlateSolveResult, error) {
		frameID, frameErr := uuid.Parse(input.FrameID)
		if frameErr != nil || frameID.String() != input.FrameID || (input.ApproxRAHours == nil) != (input.ApproxDecDegrees == nil) || input.ApproxRAHours != nil && (!finite(*input.ApproxRAHours) || !finite(*input.ApproxDecDegrees) || *input.ApproxRAHours < 0 || *input.ApproxRAHours >= 24 || *input.ApproxDecDegrees < -90 || *input.ApproxDecDegrees > 90) {
			return ara.PlateSolveResult{}, fmt.Errorf("%w: frame ID and an optional complete in-range coordinate hint are required", errInvalidArgument)
		}
		var hint *[2]float64
		if input.ApproxRAHours != nil {
			hint = &[2]float64{*input.ApproxRAHours, *input.ApproxDecDegrees}
		}
		solution, _, err := client.SolveFrameWithRequestID(ctx, frameID.String(), hint, requestID(ctx))
		return solution, err
	})
	if control == nil {
		return
	}
	addTool(server, instrumentation, &mcp.Tool{
		Name: "center_on_coordinates", Description: "Ask Ara to slew and iteratively plate-solve/center at RA hours and declination degrees. Requires control; returns only an accepted job receipt. Observe with get_job_status; HTTP 202 is not completion.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"control_id": stringSchema("Current control ID from begin_control"), "intent_id": stringSchema("Stable ID for this logical action; reused IDs never dispatch twice"),
			"ra_hours": {Type: "number", Description: "Target right ascension in hours, [0,24)"}, "dec_degrees": {Type: "number", Description: "Target declination in degrees, [-90,90]"},
		}, "control_id", "intent_id", "ra_hours", "dec_degrees"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CenterInput) (CenterResult, error) {
		return centerCoordinates(ctx, client, control, input)
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "cancel_centering_job", Description: "Request cancellation of an Ara centering job through its job ID and return an immediate Ara job-state observation. A cancelled job does not by itself prove the mount is stationary.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{"control_id": stringSchema("Current control ID"), "intent_id": stringSchema("Stable cancellation intent ID"), "job_id": stringSchema("Ara centering job ID")}, "control_id", "intent_id", "job_id"),
		Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input CancelCenterInput) (CenterCancelResult, error) {
		return cancelCenteringJob(ctx, client, control, input)
	})
}

func validateCenterInput(input CenterInput) error {
	if !finite(input.RAHours) || !finite(input.DecDegrees) || input.RAHours < 0 || input.RAHours >= 24 || input.DecDegrees < -90 || input.DecDegrees > 90 {
		return errors.New("RA hours must be [0,24) and declination degrees [-90,90], both finite")
	}
	return nil
}

func centerCoordinates(ctx context.Context, client *ara.Client, control *ControlManager, input CenterInput) (CenterResult, error) {
	if err := validateCenterInput(input); err != nil {
		return CenterResult{}, fmt.Errorf("%w: %w", errInvalidArgument, err)
	}
	arguments, err := json.Marshal(struct {
		RA  float64 `json:"ra_hours"`
		Dec float64 `json:"dec_degrees"`
	}{input.RAHours, input.DecDegrees})
	if err != nil {
		return CenterResult{}, err
	}
	var output CenterResult
	receipt, err := control.DispatchMutation(ctx, MutationRequest{
		ControlID: input.ControlID, IntentID: input.IntentID, Operation: "center_on_coordinates", Arguments: arguments, Kind: MutationManual,
		Preflight: func(ctx context.Context) (RunSnapshot, error) {
			if _, err := readConnectedDevice(ctx, client, ara.DeviceTelescope); err != nil {
				return RunSnapshot{}, fmt.Errorf("centering requires a connected telescope: %w", err)
			}
			return currentRunSnapshot(ctx, client), nil
		},
	}, func(ctx context.Context) (MutationReceipt, error) {
		job, result, callErr := client.CenterWithRequestID(ctx, input.RAHours, input.DecDegrees, requestID(ctx))
		output.Job = job
		mutation := MutationReceipt{Outcome: result.Outcome, ReceiptID: job.JobID, RetrySafe: false}
		if callErr == nil && (result.Status != 202 || result.Outcome != ara.OutcomeAccepted || job.JobID == "" || job.JobType != "center") {
			mutation.Outcome, mutation.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
			callErr = errors.New("Ara center response was not a valid accepted center job")
		}
		if callErr != nil {
			mutation.ErrorClass = toolErrorClass(callErr)
		}
		return mutation, callErr
	})
	output.Mutation = receipt
	if err != nil && receipt.Outcome != ara.OutcomeUnknown {
		return CenterResult{}, sequenceMutationError("center telescope", receipt, err)
	}
	return output, nil
}

func cancelCenteringJob(ctx context.Context, client *ara.Client, control *ControlManager, input CancelCenterInput) (CenterCancelResult, error) {
	if input.JobID == "" {
		return CenterCancelResult{}, fmt.Errorf("%w: job ID is required", errInvalidArgument)
	}
	arguments, err := json.Marshal(struct {
		JobID string `json:"job_id"`
	}{input.JobID})
	if err != nil {
		return CenterCancelResult{}, err
	}
	receipt, dispatchErr := control.DispatchMutation(ctx, MutationRequest{ControlID: input.ControlID, IntentID: input.IntentID, Operation: "cancel_centering_job", Arguments: arguments, Kind: MutationInterrupt}, func(ctx context.Context) (MutationReceipt, error) {
		job, result, err := client.GetJobWithRequestID(ctx, input.JobID, requestID(ctx))
		if err != nil {
			return araMutationReceipt(result, err), err
		}
		if job.JobType != "center" {
			return MutationReceipt{Outcome: ara.OutcomeFailed, ErrorClass: "invalid_argument"}, fmt.Errorf("%w: job is not a centering job", errInvalidArgument)
		}
		if job.State != "queued" && job.State != "running" {
			return MutationReceipt{Outcome: ara.OutcomeFailed, ErrorClass: "run_not_active"}, fmt.Errorf("%w: centering job is already %s", errRunInactive, job.State)
		}
		result, err = client.CancelJobWithRequestID(ctx, input.JobID, requestID(ctx))
		mutation := MutationReceipt{Outcome: ara.OutcomeAccepted, ReceiptID: input.JobID}
		if err != nil {
			mutation = araMutationReceipt(result, err)
		} else if result.Status != http.StatusNoContent {
			err = errors.New("Ara job cancellation returned an unexpected status")
			mutation.Outcome, mutation.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
		}
		if err != nil {
			mutation.ErrorClass = toolErrorClass(err)
		}
		return mutation, err
	})
	if dispatchErr != nil && receipt.Outcome != ara.OutcomeUnknown {
		return CenterCancelResult{}, fmt.Errorf("cancel Ara centering job: %w", dispatchErr)
	}
	output := CenterCancelResult{Mutation: receipt}
	if receipt.Replayed {
		output.ObservationError = "replayed_receipt_only"
		return output, nil
	}
	job, _, observationErr := client.GetJobWithRequestID(ctx, input.JobID, requestID(ctx))
	if observationErr != nil {
		output.ObservationError = toolErrorClass(observationErr)
	} else {
		output.Job = &job
	}
	return output, nil
}
