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

var (
	errEquipmentUnavailable = errors.New("ara equipment is disconnected or unavailable")
	errUnsupportedEquipment = errors.New("ara equipment does not support the requested action")
)

type equipmentInput struct {
	ControlID           string   `json:"control_id"`
	IntentID            string   `json:"intent_id"`
	ExposureSec         float64  `json:"exposure_sec,omitempty"`
	Gain                *int     `json:"gain,omitempty"`
	BinX                *int     `json:"bin_x,omitempty"`
	BinY                *int     `json:"bin_y,omitempty"`
	OffsetX             *int     `json:"offset_x,omitempty"`
	OffsetY             *int     `json:"offset_y,omitempty"`
	Width               *int     `json:"width,omitempty"`
	Height              *int     `json:"height,omitempty"`
	FilterName          string   `json:"filter_name,omitempty"`
	CameraOffset        *int     `json:"camera_offset,omitempty"`
	Enabled             *bool    `json:"enabled,omitempty"`
	TargetTemperatureC  *float64 `json:"target_temperature_c,omitempty"`
	RightAscensionHours *float64 `json:"right_ascension_hours,omitempty"`
	DeclinationDegrees  *float64 `json:"declination_degrees,omitempty"`
	Sync                bool     `json:"sync,omitempty"`
	Reason              string   `json:"reason,omitempty"`
	TargetPosition      *int     `json:"target_position,omitempty"`
	UseTempComp         bool     `json:"use_temp_comp,omitempty"`
	Position            *int     `json:"position,omitempty"`
	DitherPixels        *float64 `json:"pixels,omitempty"`
}

// EquipmentActionResult keeps Ara's acceptance receipt separate from its result
// data and immediate device observation.
type EquipmentActionResult struct {
	Mutation         MutationReceipt          `json:"mutation"`
	Frame            ara.ExposureResponse     `json:"frame,omitzero"`
	Job              ara.BatchJob             `json:"job,omitzero"`
	DeviceState      jsontext.Value           `json:"device_state,omitempty"`
	ObservationError string                   `json:"observation_error,omitempty"`
	EmergencyStop    *ara.EmergencyStopResult `json:"emergency_stop,omitempty"`
}

func registerManualEquipmentTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	if control == nil {
		return
	}
	register := func(name, description, device string, fields map[string]*jsonschema.Schema, required []string, interrupt bool) {
		properties := map[string]*jsonschema.Schema{
			"control_id": stringSchema("Current control ID from begin_control"),
			"intent_id":  stringSchema("Stable ID for this logical action; reused IDs never dispatch twice"),
		}
		for key, value := range fields {
			properties[key] = value
		}
		required = append([]string{"control_id", "intent_id"}, required...)
		addTool(server, instrumentation, &mcp.Tool{
			Name: name, Description: description,
			InputSchema: objectSchema(properties, required...),
			OutputSchema: objectSchema(map[string]*jsonschema.Schema{
				"mutation": objectSchema(map[string]*jsonschema.Schema{
					"outcome":     stringSchema("Ara operation outcome: accepted, completed, failed, or unknown"),
					"receipt_id":  stringSchema("Ara frame, job, or operation receipt identifier when supplied"),
					"error_class": stringSchema("Bounded mutation error category"),
					"retry_safe":  {Type: "boolean"}, "replayed": {Type: "boolean"},
				}),
				"frame":             {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
				"job":               {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
				"device_state":      {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
				"observation_error": stringSchema("Bounded device observation error, if unavailable"),
				"emergency_stop":    {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
			}, "mutation"),
			Annotations: &mcp.ToolAnnotations{OpenWorldHint: new(false)},
		}, func(ctx context.Context, _ *mcp.CallToolRequest, input equipmentInput) (EquipmentActionResult, error) {
			return runEquipmentAction(ctx, instrumentation, client, control, name, device, input, interrupt)
		})
	}
	register("capture_exposure", "Start a camera exposure (seconds). Ara returns a frame ID when capture is accepted; inspect that frame separately to confirm persistence.", "camera", map[string]*jsonschema.Schema{
		"exposure_sec": {Type: "number", Description: "Exposure duration in seconds; checked against camera limits"},
		"gain":         {Type: "integer", Description: "Optional camera gain; checked against camera limits"},
		"bin_x":        {Type: "integer", Description: "Horizontal binning; defaults to 1"}, "bin_y": {Type: "integer", Description: "Vertical binning; defaults to 1"},
		"offset_x":      {Type: "integer", Description: "Optional horizontal subframe offset in pixels"},
		"offset_y":      {Type: "integer", Description: "Optional vertical subframe offset in pixels"},
		"width":         {Type: "integer", Description: "Optional subframe width in pixels"},
		"height":        {Type: "integer", Description: "Optional subframe height in pixels"},
		"filter_name":   stringSchema("Optional name of a filter on the connected wheel"),
		"camera_offset": {Type: "integer", Description: "Optional electronic camera offset in device units; distinct from pixel subframe offsets"},
	}, []string{"exposure_sec"}, false)
	register("abort_exposure", "Request Ara to abort the current camera exposure. Acceptance does not prove that a frame was interrupted.", "camera", nil, nil, true)
	register("set_camera_cooler", "Set the camera cooler on or off and optionally set target temperature in degrees Celsius. A target is ignored when disabling; omission leaves the setpoint unchanged.", "camera", map[string]*jsonschema.Schema{
		"enabled": {Type: "boolean"}, "target_temperature_c": {Type: "number", Description: "Optional target temperature in degrees Celsius"},
	}, []string{"enabled"}, false)
	register("slew_telescope", "Ask Ara to slew to right ascension in hours and declination in degrees. The result is acceptance plus an immediate mount observation, not proof the slew finished.", "telescope", map[string]*jsonschema.Schema{
		"right_ascension_hours": {Type: "number", Description: "Right ascension in hours, 0 inclusive to 24 exclusive"},
		"declination_degrees":   {Type: "number", Description: "Declination in degrees, -90 to 90 inclusive"}, "sync": {Type: "boolean", Description: "Sync instead of slew; defaults to false"},
	}, []string{"right_ascension_hours", "declination_degrees"}, false)
	register("park_telescope", "Ask Ara to park the telescope. The result is acceptance plus an immediate mount observation, not proof parking finished.", "telescope", map[string]*jsonschema.Schema{"reason": stringSchema("Optional reason")}, nil, false)
	register("unpark_telescope", "Ask Ara to unpark the telescope. The result is acceptance plus an immediate mount observation, not proof unparking finished.", "telescope", nil, nil, false)
	register("abort_telescope_slew", "Interrupt telescope motion through Ara. Ara also pauses active sequences; the accepted response is not proof the mount has stopped.", "telescope", nil, nil, true)
	register("move_focuser", "Ask Ara to move the focuser to a device position in focuser steps. The result is acceptance plus an immediate focuser observation, not proof motion finished.", "focuser", map[string]*jsonschema.Schema{"target_position": {Type: "integer", Description: "Target focuser position in device steps"}, "use_temp_comp": {Type: "boolean", Description: "Use temperature compensation; defaults to false"}}, []string{"target_position"}, false)
	register("run_autofocus", "Start Ara's autofocus background job and return its job ID. Job-state monitoring is not included yet.", "focuser", nil, nil, false)
	register("select_filter", "Ask Ara to select a filter-wheel slot by its current position index. The result is acceptance plus an immediate wheel observation, not proof selection finished.", "filterwheel", map[string]*jsonschema.Schema{"position": {Type: "integer", Description: "Slot position from filter-wheel status"}}, []string{"position"}, false)
	register("start_guiding", "Ask Ara to start guiding. The accepted operation is not proof that guiding has started; inspect guider status.", "guider", nil, nil, false)
	register("stop_guiding", "Ask Ara to stop guiding. The accepted operation is not proof that guiding has stopped; inspect guider status.", "guider", nil, nil, false)
	register("dither_guiding", "Ask Ara to dither the guider by the requested amplitude in pixels. The accepted operation is not proof that dithering has finished.", "guider", map[string]*jsonschema.Schema{"pixels": {Type: "number", Description: "Dither amplitude in pixels; must be finite and positive"}}, []string{"pixels"}, false)
	register("emergency_stop", "Run Ara's synchronous best-effort emergency-stop ladder. The response reports which rungs Ara completed or failed; it can abort sequences, exposures, guiding, park the mount, and switch off a flat panel.", "", nil, nil, true)
}

func runEquipmentAction(ctx context.Context, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager, operation, device string, input equipmentInput, interrupt bool) (EquipmentActionResult, error) {
	if err := validateEquipmentInput(operation, &input); err != nil {
		return EquipmentActionResult{}, fmt.Errorf("%w: %w", errInvalidArgument, err)
	}
	arguments, err := json.Marshal(struct {
		Device string         `json:"device"`
		Input  equipmentInput `json:"input"`
	}{device, input})
	if err != nil {
		return EquipmentActionResult{}, fmt.Errorf("encode equipment intent: %w", err)
	}
	kind := MutationManual
	if interrupt {
		kind = MutationInterrupt
	}
	mutationRequest := MutationRequest{ControlID: input.ControlID, IntentID: input.IntentID, Operation: operation, Arguments: arguments, Kind: kind}
	if kind == MutationManual {
		mutationRequest.Preflight = func(ctx context.Context) (RunSnapshot, error) {
			if device != "" {
				if err := preflightEquipment(ctx, client, operation, device, input); err != nil {
					return RunSnapshot{}, err
				}
			}
			return currentRunSnapshot(ctx, client), nil
		}
	}
	var output EquipmentActionResult
	receipt, dispatchErr := control.DispatchMutation(ctx, mutationRequest, func(ctx context.Context) (MutationReceipt, error) {
		var result ara.Result
		var err error
		switch operation {
		case "capture_exposure":
			output.Frame, result, err = client.StartExposureWithRequestID(ctx, ara.ExposureRequest{
				ExposureSec: input.ExposureSec, Gain: input.Gain, BinX: equipmentBin(input.BinX), BinY: equipmentBin(input.BinY),
				OffsetX: input.OffsetX, OffsetY: input.OffsetY, Width: input.Width, Height: input.Height,
				FilterName: input.FilterName, CameraOffset: input.CameraOffset,
			}, requestID(ctx))
		case "abort_exposure":
			result, err = client.AbortExposureWithRequestID(ctx, requestID(ctx))
		case "set_camera_cooler":
			result, err = client.SetCameraCoolerWithRequestID(ctx, *input.Enabled, input.TargetTemperatureC, requestID(ctx))
		case "slew_telescope":
			result, err = client.SlewTelescopeWithRequestID(ctx, *input.RightAscensionHours, *input.DeclinationDegrees, input.Sync, requestID(ctx))
		case "park_telescope":
			result, err = client.ParkTelescopeWithRequestID(ctx, input.Reason, requestID(ctx))
		case "unpark_telescope":
			result, err = client.UnparkTelescopeWithRequestID(ctx, requestID(ctx))
		case "abort_telescope_slew":
			result, err = client.AbortTelescopeSlewWithRequestID(ctx, requestID(ctx))
		case "move_focuser":
			result, err = client.MoveFocuserWithRequestID(ctx, *input.TargetPosition, input.UseTempComp, requestID(ctx))
		case "run_autofocus":
			output.Job, result, err = client.RunAutofocusWithRequestID(ctx, requestID(ctx))
		case "select_filter":
			result, err = client.SelectFilterWithRequestID(ctx, *input.Position, requestID(ctx))
		case "start_guiding":
			result, err = client.StartGuidingWithRequestID(ctx, requestID(ctx))
		case "stop_guiding":
			result, err = client.StopGuidingWithRequestID(ctx, requestID(ctx))
		case "dither_guiding":
			result, err = client.DitherGuiderWithRequestID(ctx, *input.DitherPixels, requestID(ctx))
		case "emergency_stop":
			var stop ara.EmergencyStopResult
			stop, result, err = client.EmergencyStopWithRequestID(ctx, requestID(ctx))
			output.EmergencyStop = &stop
		}
		receipt := araMutationReceipt(result, err)
		if err == nil && result.Outcome != ara.OutcomeAccepted && operation != "emergency_stop" {
			err = errors.New("Ara equipment action returned a non-accepted response")
			receipt.Outcome, receipt.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
		}
		if err == nil && operation == "capture_exposure" && output.Frame.FrameID == "" {
			err = errors.New("Ara exposure response omitted its frame ID")
			receipt.Outcome, receipt.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
		}
		if err == nil && operation == "run_autofocus" && output.Job.JobID == "" {
			err = errors.New("Ara autofocus response omitted its job ID")
			receipt.Outcome, receipt.ErrorClass = ara.OutcomeUnknown, "invalid_upstream_response"
		}
		if err != nil {
			receipt.ErrorClass = toolErrorClass(err)
		}
		if operation == "capture_exposure" {
			receipt.ReceiptID = output.Frame.FrameID
		}
		if operation == "run_autofocus" {
			receipt.ReceiptID = output.Job.JobID
		}
		return receipt, err
	})
	output.Mutation = receipt
	if dispatchErr != nil {
		if receipt.Outcome != ara.OutcomeUnknown {
			return EquipmentActionResult{}, fmt.Errorf("Ara %s failed (%s): %w", operation, boundedMutationErrorClass(receipt.ErrorClass), dispatchErr)
		}
		logEquipmentAction(ctx, instrumentation, operation, receipt)
		return output, nil
	}
	if receipt.Replayed {
		// Receipt replay intentionally does not re-run an action or fabricate the original payload.
		output.ObservationError = "replayed_receipt_only"
	}
	if device != "" && receipt.Outcome == ara.OutcomeAccepted && !receipt.Replayed {
		state, _, stateErr := client.GetDeviceStatusWithRequestID(ctx, ara.DeviceType(device), requestID(ctx))
		if stateErr != nil {
			output.ObservationError = toolErrorClass(stateErr)
		} else {
			output.DeviceState = state
		}
	}
	logEquipmentAction(ctx, instrumentation, operation, receipt)
	return output, nil
}

func logEquipmentAction(ctx context.Context, instrumentation toolInstrumentation, operation string, receipt MutationReceipt) {
	level, message := slog.LevelInfo, "Ara equipment action accepted"
	switch receipt.Outcome {
	case ara.OutcomeCompleted:
		message = "Ara equipment action completed"
	case ara.OutcomeUnknown:
		level, message = slog.LevelWarn, "Ara equipment action outcome unknown"
	}
	fields := []any{"service", "ara-mcp", "version", instrumentation.version, "component", "equipment", "event", "equipment_action",
		"transport", instrumentation.transport, "request_id", requestID(ctx), "action", operation, "outcome", receipt.Outcome}
	if receipt.ReceiptID != "" {
		fields = append(fields, "receipt_id", receipt.ReceiptID)
	}
	instrumentation.logger.Log(ctx, level, message, fields...)
}

func validateEquipmentInput(operation string, input *equipmentInput) error {
	switch operation {
	case "capture_exposure":
		if !finite(input.ExposureSec) || input.ExposureSec <= 0 || invalidOptionalPositive(input.BinX) || invalidOptionalPositive(input.BinY) ||
			invalidOptionalNonnegative(input.Gain) || invalidOptionalNonnegative(input.OffsetX) || invalidOptionalNonnegative(input.OffsetY) ||
			invalidOptionalPositive(input.Width) || invalidOptionalPositive(input.Height) || invalidOptionalNonnegative(input.CameraOffset) {
			return errors.New("exposure and subframe values must be positive and gain/offset values non-negative")
		}
	case "set_camera_cooler":
		if input.Enabled == nil || input.TargetTemperatureC != nil && !finite(*input.TargetTemperatureC) {
			return errors.New("enabled and a finite optional target temperature are required")
		}
	case "slew_telescope":
		if input.RightAscensionHours == nil || input.DeclinationDegrees == nil || !finite(*input.RightAscensionHours) || !finite(*input.DeclinationDegrees) ||
			*input.RightAscensionHours < 0 || *input.RightAscensionHours >= 24 || *input.DeclinationDegrees < -90 || *input.DeclinationDegrees > 90 {
			return errors.New("right ascension must be [0,24) hours and declination [-90,90] degrees")
		}
	case "move_focuser":
		if input.TargetPosition == nil || *input.TargetPosition < 0 {
			return errors.New("target position must be non-negative")
		}
	case "select_filter":
		if input.Position == nil || *input.Position < 0 {
			return errors.New("filter position must be non-negative")
		}
	case "dither_guiding":
		if input.DitherPixels == nil || !finite(*input.DitherPixels) || *input.DitherPixels <= 0 {
			return errors.New("dither amplitude must be finite and positive pixels")
		}
	}
	return nil
}

func preflightEquipment(ctx context.Context, client *ara.Client, operation, device string, input equipmentInput) error {
	body, err := readConnectedDevice(ctx, client, ara.DeviceType(device))
	if err != nil {
		if errors.Is(err, errInvalidArgument) {
			return fmt.Errorf("%w: %s is not connected", errEquipmentUnavailable, device)
		}
		if apiError, ok := errors.AsType[*ara.APIError](err); ok && apiError.Status == 404 {
			return fmt.Errorf("%w: Ara has no connected %s device", errEquipmentUnavailable, device)
		}
		return err
	}
	switch operation {
	case "capture_exposure":
		var camera struct {
			Capabilities *cameraCapabilities `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &camera); err != nil || camera.Capabilities == nil {
			return fmt.Errorf("%w: Ara camera capabilities are unavailable", errRunStateUnknown)
		}
		caps := *camera.Capabilities
		if err := validateCameraExecutionRequirements(caps, []sequenceExposureRequirement{{Exposure: input.ExposureSec, Gain: input.Gain, Offset: input.CameraOffset, BinX: equipmentBin(input.BinX), BinY: equipmentBin(input.BinY)}}); err != nil {
			return err
		}
		if input.Width != nil && (caps.SensorWidth == nil || *input.Width > *caps.SensorWidth) ||
			input.OffsetX != nil && (caps.SensorWidth == nil || *input.OffsetX >= *caps.SensorWidth || input.Width != nil && *input.OffsetX > *caps.SensorWidth-*input.Width) {
			return fmt.Errorf("%w: exposure subframe exceeds or cannot be checked against sensor width", errInvalidArgument)
		}
		if input.Height != nil && (caps.SensorHeight == nil || *input.Height > *caps.SensorHeight) ||
			input.OffsetY != nil && (caps.SensorHeight == nil || *input.OffsetY >= *caps.SensorHeight || input.Height != nil && *input.OffsetY > *caps.SensorHeight-*input.Height) {
			return fmt.Errorf("%w: exposure subframe exceeds or cannot be checked against sensor height", errInvalidArgument)
		}
		if input.FilterName != "" {
			wheel, wheelErr := readConnectedDevice(ctx, client, ara.DeviceFilterWheel)
			if wheelErr != nil {
				return fmt.Errorf("filter selection requires a connected filter wheel: %w", wheelErr)
			}
			var filters struct {
				Slots []struct {
					Name string `json:"name"`
				} `json:"slots"`
			}
			if json.Unmarshal(wheel, &filters) != nil || len(filters.Slots) == 0 {
				return fmt.Errorf("%w: Ara filter-wheel slots are unavailable", errRunStateUnknown)
			}
			found := false
			for _, slot := range filters.Slots {
				found = found || strings.EqualFold(strings.TrimSpace(slot.Name), strings.TrimSpace(input.FilterName))
			}
			if !found {
				return fmt.Errorf("%w: filter name is not available on the connected wheel", errUnsupportedEquipment)
			}
		}
	case "set_camera_cooler":
		var camera struct {
			Capabilities struct {
				HasCooler         bool `json:"has_cooler"`
				CanSetTemperature bool `json:"can_set_temperature"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &camera); err != nil {
			return fmt.Errorf("%w: Ara camera cooler capabilities are unavailable", errRunStateUnknown)
		}
		if !camera.Capabilities.HasCooler || input.TargetTemperatureC != nil && (!*input.Enabled || !camera.Capabilities.CanSetTemperature) {
			return fmt.Errorf("%w: camera does not support the requested cooler operation", errUnsupportedEquipment)
		}
	case "slew_telescope":
		var mount struct {
			Capabilities struct {
				CanSlew bool `json:"can_slew"`
				CanSync bool `json:"can_sync"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &mount); err != nil {
			return fmt.Errorf("%w: Ara telescope capabilities are unavailable", errRunStateUnknown)
		}
		if input.Sync && !mount.Capabilities.CanSync || !input.Sync && !mount.Capabilities.CanSlew {
			return fmt.Errorf("%w: telescope does not support the requested slew operation", errUnsupportedEquipment)
		}
	case "park_telescope", "unpark_telescope":
		var mount struct {
			Capabilities struct {
				CanPark   bool `json:"can_park"`
				CanUnpark bool `json:"can_unpark"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &mount); err != nil {
			return fmt.Errorf("%w: Ara telescope capabilities are unavailable", errRunStateUnknown)
		}
		if operation == "park_telescope" && !mount.Capabilities.CanPark || operation == "unpark_telescope" && !mount.Capabilities.CanUnpark {
			return fmt.Errorf("%w: telescope does not support %s", errUnsupportedEquipment, operation)
		}
	case "move_focuser":
		var focuser struct {
			Capabilities struct {
				MinPosition int `json:"min_position"`
				MaxPosition int `json:"max_position"`
			} `json:"capabilities"`
		}
		if err := json.Unmarshal(body, &focuser); err != nil || focuser.Capabilities.MaxPosition < focuser.Capabilities.MinPosition {
			return fmt.Errorf("%w: Ara focuser limits are unavailable", errRunStateUnknown)
		}
		if *input.TargetPosition < focuser.Capabilities.MinPosition || *input.TargetPosition > focuser.Capabilities.MaxPosition {
			return fmt.Errorf("%w: target position is outside Ara focuser limits", errInvalidArgument)
		}
	case "select_filter":
		var wheel struct {
			Slots []struct {
				Position int `json:"position"`
			} `json:"slots"`
		}
		if err := json.Unmarshal(body, &wheel); err != nil {
			return fmt.Errorf("%w: Ara filter-wheel slots are unavailable", errRunStateUnknown)
		}
		found := false
		for _, slot := range wheel.Slots {
			found = found || slot.Position == *input.Position
		}
		if !found {
			return fmt.Errorf("%w: filter position is not available", errUnsupportedEquipment)
		}
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func invalidOptionalNonnegative(value *int) bool { return value != nil && *value < 0 }

func invalidOptionalPositive(value *int) bool { return value != nil && *value < 1 }

func equipmentBin(value *int) int {
	if value == nil {
		return 1
	}
	return *value
}
