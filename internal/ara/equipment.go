// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"strconv"
)

// ExposureRequest contains the fields accepted by Ara's camera exposure route.
type ExposureRequest struct {
	ExposureSec  float64 `json:"exposure_sec"`
	Gain         *int    `json:"gain"`
	BinX         int     `json:"bin_x"`
	BinY         int     `json:"bin_y"`
	OffsetX      *int    `json:"offset_x,omitempty"`
	OffsetY      *int    `json:"offset_y,omitempty"`
	Width        *int    `json:"width,omitempty"`
	Height       *int    `json:"height,omitempty"`
	FilterName   string  `json:"filter_name,omitempty"`
	CameraOffset *int    `json:"camera_offset,omitempty"`
}

// ExposureResponse acknowledges capture and identifies the frame to inspect.
type ExposureResponse struct {
	FrameID     string  `json:"frame_id"`
	PreviewURL  string  `json:"preview_url"`
	ExposureSec float64 `json:"exposure_sec"`
	CapturedAt  string  `json:"captured_at"`
}

// BatchJob is Ara's job receipt for operations such as autofocus.
type BatchJob struct {
	JobID       string `json:"job_id"`
	JobType     string `json:"job_type"`
	State       string `json:"state"`
	Done        int    `json:"done"`
	Total       int    `json:"total"`
	StartedUTC  string `json:"started_utc"`
	FinishedUTC string `json:"finished_utc,omitempty"`
	Error       string `json:"error_message,omitempty"`
}

// EmergencyStopResult reports Ara's best-effort stop ladder, rung by rung.
type EmergencyStopResult struct {
	AlreadyInProgress bool     `json:"already_in_progress"`
	RunsAborted       int      `json:"runs_aborted"`
	ExposureAborted   bool     `json:"exposure_aborted"`
	GuidingStopped    bool     `json:"guiding_stopped"`
	ParkRequested     bool     `json:"park_requested"`
	FlatPanelLightOff bool     `json:"flat_panel_light_off"`
	FailedRungs       []string `json:"failed_rungs"`
}

// StartExposureWithRequestID requests a manual camera exposure through Ara.
func (c *Client) StartExposureWithRequestID(ctx context.Context, input ExposureRequest, requestID string) (ExposureResponse, Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return ExposureResponse{}, Result{Outcome: OutcomeFailed}, err
	}
	var response ExposureResponse
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/camera/exposure", Body: body, RequestID: requestID}, &response)
	return response, result, err
}

// AbortExposureWithRequestID asks Ara to abort a camera exposure.
func (c *Client) AbortExposureWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/camera/exposure/abort", RequestID: requestID}, nil)
}

// SetCameraCoolerWithRequestID changes cooler state and, when supplied, its target in Celsius.
func (c *Client) SetCameraCoolerWithRequestID(ctx context.Context, enabled bool, targetTemperatureC *float64, requestID string) (Result, error) {
	if !enabled {
		targetTemperatureC = nil
	}
	body, err := json.Marshal(struct {
		Enabled            bool     `json:"enabled"`
		TargetTemperatureC *float64 `json:"target_temperature_c,omitempty"`
	}{enabled, targetTemperatureC})
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/camera/cooler", Body: body, RequestID: requestID}, nil)
}

// SlewTelescopeWithRequestID requests a slew or sync (RA hours, declination degrees).
func (c *Client) SlewTelescopeWithRequestID(ctx context.Context, rightAscensionHours, declinationDegrees float64, sync bool, requestID string) (Result, error) {
	body, err := json.Marshal(struct {
		RightAscensionHours float64 `json:"right_ascension_hours"`
		DeclinationDegrees  float64 `json:"declination_degrees"`
		Sync                bool    `json:"sync"`
	}{rightAscensionHours, declinationDegrees, sync})
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/telescope/slew", Body: body, RequestID: requestID}, nil)
}

// ParkTelescopeWithRequestID requests telescope parking.
func (c *Client) ParkTelescopeWithRequestID(ctx context.Context, reason, requestID string) (Result, error) {
	body, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{reason})
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/telescope/park", Body: body, RequestID: requestID}, nil)
}

// UnparkTelescopeWithRequestID asks Ara to unpark the telescope.
func (c *Client) UnparkTelescopeWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/telescope/unpark", RequestID: requestID}, nil)
}

// AbortTelescopeSlewWithRequestID interrupts telescope motion and Ara's active sequence pause behavior.
func (c *Client) AbortTelescopeSlewWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/telescope/abort", RequestID: requestID}, nil)
}

// MoveFocuserWithRequestID requests a move to a device position.
func (c *Client) MoveFocuserWithRequestID(ctx context.Context, targetPosition int, useTempComp bool, requestID string) (Result, error) {
	body, err := json.Marshal(struct {
		TargetPosition int  `json:"target_position"`
		UseTempComp    bool `json:"use_temp_comp"`
	}{targetPosition, useTempComp})
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/focuser/move", Body: body, RequestID: requestID}, nil)
}

// RunAutofocusWithRequestID starts Ara's autofocus job.
func (c *Client) RunAutofocusWithRequestID(ctx context.Context, requestID string) (BatchJob, Result, error) {
	var response BatchJob
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/focuser/autofocus", RequestID: requestID}, &response)
	return response, result, err
}

// SelectFilterWithRequestID requests a filter-wheel slot selection.
func (c *Client) SelectFilterWithRequestID(ctx context.Context, position int, requestID string) (Result, error) {
	body, err := json.Marshal(struct {
		Position int `json:"position"`
	}{position})
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/filterwheel/change", Body: body, RequestID: requestID}, nil)
}

// StartGuidingWithRequestID asks Ara to schedule guider start.
func (c *Client) StartGuidingWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/guider/start", RequestID: requestID}, nil)
}

// StopGuidingWithRequestID asks Ara to schedule guider stop.
func (c *Client) StopGuidingWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/guider/stop", RequestID: requestID}, nil)
}

// DitherGuiderWithRequestID requests guiding dither amplitude in pixels.
func (c *Client) DitherGuiderWithRequestID(ctx context.Context, pixels float64, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/guider/dither", Query: map[string][]string{"pixels": {strconv.FormatFloat(pixels, 'f', -1, 64)}}, RequestID: requestID}, nil)
}

// EmergencyStopWithRequestID executes Ara's synchronous best-effort stop ladder.
func (c *Client) EmergencyStopWithRequestID(ctx context.Context, requestID string) (EmergencyStopResult, Result, error) {
	var response EmergencyStopResult
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/server/emergency-stop", RequestID: requestID}, &response)
	return response, result, err
}
