// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"
)

type AutofocusProbe struct {
	Index    int     `json:"index"`
	Phase    string  `json:"phase"`
	Position int     `json:"position"`
	HFR      float64 `json:"hfr"`
	Stars    int     `json:"stars"`
	Kept     bool    `json:"kept"`
}

type AutofocusCurvePoint struct {
	Position float64 `json:"position"`
	HFR      float64 `json:"hfr"`
}

type AutofocusCurveFit struct {
	Algorithm          string                `json:"algorithm"`
	RSquared           float64               `json:"r_squared"`
	BestPosition       float64               `json:"best_position"`
	PredictedHFR       float64               `json:"predicted_hfr"`
	WithinSampledRange bool                  `json:"within_sampled_range"`
	Curve              []AutofocusCurvePoint `json:"curve"`
}

// AutofocusRun is Ara's bounded current/most-recent run snapshot.
type AutofocusRun struct {
	State               string             `json:"state"`
	Mode                *string            `json:"mode"`
	Phase               *string            `json:"phase"`
	Trigger             *string            `json:"trigger"`
	StartedUTC          *time.Time         `json:"started_utc"`
	CompletedUTC        *time.Time         `json:"completed_utc"`
	DurationSeconds     *float64           `json:"duration_seconds"`
	StartPosition       *int               `json:"start_position"`
	FinalPosition       *int               `json:"final_position"`
	FinalHFR            *float64           `json:"final_hfr"`
	FinalStars          *int               `json:"final_stars"`
	Filter              *string            `json:"filter"`
	FocuserTemperatureC *float64           `json:"focuser_temperature_c"`
	TotalSteps          int                `json:"total_steps"`
	CompletedSteps      int                `json:"completed_steps"`
	SweepAttempt        int                `json:"sweep_attempt"`
	StepSize            *int               `json:"step_size"`
	StepSizeSource      *string            `json:"step_size_source"`
	Probes              []AutofocusProbe   `json:"probes"`
	Fit                 *AutofocusCurveFit `json:"fit"`
	Reason              *string            `json:"reason"`
	RestoredPosition    *int               `json:"restored_position"`
	HasFrame            bool               `json:"has_frame"`
	FrameSeq            int64              `json:"frame_seq"`
	FramePosition       *int               `json:"frame_position"`
	FrameHFR            *float64           `json:"frame_hfr"`
}

type AutofocusCalibrationSample struct {
	FocuserPosition          int     `json:"focuser_position"`
	StarCount                int     `json:"star_count"`
	MedianHFR                float64 `json:"median_hfr"`
	MedianFWHM               float64 `json:"median_fwhm"`
	MedianRoundness          float64 `json:"median_roundness"`
	MedianPeakToBackground   float64 `json:"median_peak_to_background"`
	MedianDonutOuterDiameter float64 `json:"median_donut_outer_diameter"`
	MedianDonutInnerDiameter float64 `json:"median_donut_inner_diameter"`
	MedianRingThickness      float64 `json:"median_ring_thickness"`
	MedianDonutShadowDepth   float64 `json:"median_donut_shadow_depth"`
	MedianRadialSkew         float64 `json:"median_radial_skew"`
}

// AutofocusCalibration is Ara's stored Smart Focus calibration, when available.
type AutofocusCalibration struct {
	Samples             []AutofocusCalibrationSample `json:"samples"`
	CalibratedUTC       time.Time                    `json:"calibrated_utc"`
	FocuserTemperatureC *float64                     `json:"focuser_temperature_c"`
	Filter              *string                      `json:"filter"`
	CurveHalfWidthSteps *float64                     `json:"curve_half_width_steps"`
	InFocusHFR          *float64                     `json:"in_focus_hfr"`
}

// GetAutofocusStateWithRequestID reads Ara's current or most recent autofocus run.
func (c *Client) GetAutofocusStateWithRequestID(ctx context.Context, requestID string) (AutofocusRun, Result, error) {
	var run AutofocusRun
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/autofocus/state", RequestID: requestID}, &run)
	return run, result, err
}

// GetAutofocusCalibrationWithRequestID reads Ara's stored calibration (404 means uncalibrated).
func (c *Client) GetAutofocusCalibrationWithRequestID(ctx context.Context, requestID string) (AutofocusCalibration, Result, error) {
	var calibration AutofocusCalibration
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/autofocus/calibration", RequestID: requestID}, &calibration)
	return calibration, result, err
}

// GetAutofocusFrameWithRequestID reads the latest rendered probe JPEG, capped at the MCP preview limit.
func (c *Client) GetAutofocusFrameWithRequestID(ctx context.Context, requestID string) ([]byte, int64, Result, error) {
	var frame []byte
	var sequenceHeader string
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/autofocus/frame", RequestID: requestID, RawResponse: true, MaxResponseBytes: maxPreviewBytes, ResponseHeader: "X-Frame-Seq", ResponseHeaderValue: &sequenceHeader}, &frame)
	if err != nil || result.Status == http.StatusNoContent {
		return nil, 0, result, err
	}
	if result.Status != http.StatusOK || len(frame) < 2 || frame[0] != 0xff || frame[1] != 0xd8 {
		return nil, 0, result, &RequestError{Class: "decode", Outcome: OutcomeFailed, cause: errors.New("Ara autofocus frame is not a JPEG response")}
	}
	frameSequence, err := strconv.ParseInt(sequenceHeader, 10, 64)
	if err != nil || frameSequence < 0 {
		return nil, 0, result, &RequestError{Class: "decode", Outcome: OutcomeFailed, cause: errors.New("Ara autofocus frame omitted a valid frame sequence")}
	}
	return frame, frameSequence, result, nil
}

// CancelAutofocusWithRequestID asks Ara to cancel the current run, whether manual or sequence-started.
func (c *Client) CancelAutofocusWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/autofocus/cancel", RequestID: requestID}, nil)
}

// RecalibrateAutofocusWithRequestID clears Ara's stored calibration so the next successful sweep rebuilds it.
func (c *Client) RecalibrateAutofocusWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/autofocus/recalibrate", RequestID: requestID}, nil)
}
