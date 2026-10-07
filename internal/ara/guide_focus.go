// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"time"
)

const maxGuideFocusFrameBytes = 1 << 20

// GuideFocusStartRequest configures Ara's guide-camera focus loop.
type GuideFocusStartRequest struct {
	ExposureSec float64 `json:"exposure_sec"`
	Binning     *int    `json:"binning,omitempty"`
}

// GuideFocusSample is one measured guide-camera frame.
type GuideFocusSample struct {
	Seq         int64     `json:"seq"`
	CapturedUTC time.Time `json:"captured_utc"`
	HFR         float64   `json:"hfr"`
	Stars       int       `json:"stars"`
	PeakADU     float64   `json:"peak_adu"`
	FWHM        float64   `json:"fwhm"`
}

// GuideFocusStatus is Ara's current or most recent guide-focus loop snapshot.
type GuideFocusStatus struct {
	Active              bool               `json:"active"`
	State               string             `json:"state"`
	ExposureSec         float64            `json:"exposure_sec"`
	Seq                 int64              `json:"seq"`
	StartedUTC          *time.Time         `json:"started_utc"`
	Latest              *GuideFocusSample  `json:"latest"`
	BestHFR             *float64           `json:"best_hfr"`
	BestSeq             *int64             `json:"best_seq"`
	Recent              []GuideFocusSample `json:"recent"`
	Error               *string            `json:"error"`
	ConsecutiveFailures int                `json:"consecutive_failures"`
	HasFrame            bool               `json:"has_frame"`
	ExpectedHFR         *float64           `json:"expected_hfr"`
	PlateScaleArcsec    *float64           `json:"plate_scale_arcsec"`
	StopReason          *string            `json:"stop_reason"`
}

// PolarAlignStatus reports whether Ara's shared guider lease is in use by alignment.
type PolarAlignStatus struct {
	Active bool   `json:"active"`
	State  string `json:"state"`
}

// StartGuideFocusWithRequestID accepts a focus loop; acceptance is not completion.
func (c *Client) StartGuideFocusWithRequestID(ctx context.Context, input GuideFocusStartRequest, requestID string) (Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return Result{Outcome: OutcomeFailed}, err
	}
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/guider/focus/start", Body: body, RequestID: requestID}, nil)
}

// StopGuideFocusWithRequestID waits for Ara's synchronous capture-drain stop.
func (c *Client) StopGuideFocusWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodPost, Route: "/equipment/guider/focus/stop", RequestID: requestID}, nil)
}

// GetGuideFocusStatusWithRequestID reads Ara's current loop snapshot.
func (c *Client) GetGuideFocusStatusWithRequestID(ctx context.Context, requestID string) (GuideFocusStatus, Result, error) {
	var status GuideFocusStatus
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/equipment/guider/focus", RequestID: requestID}, &status)
	return status, result, err
}

// GetPolarAlignStatusForGuideFocus reads the competing guider-camera lease owner.
func (c *Client) GetPolarAlignStatusForGuideFocus(ctx context.Context, requestID string) (PolarAlignStatus, Result, error) {
	var status PolarAlignStatus
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/equipment/polaralign/status", RequestID: requestID}, &status)
	return status, result, err
}

// GetGuideFocusFrameWithRequestID reads the latest JPEG, with nil for Ara's 204 no-frame response.
func (c *Client) GetGuideFocusFrameWithRequestID(ctx context.Context, requestID string) ([]byte, Result, error) {
	var frame []byte
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/equipment/guider/focus/frame", RequestID: requestID, RawResponse: true, MaxResponseBytes: maxGuideFocusFrameBytes}, &frame)
	if err != nil || result.Status == http.StatusNoContent {
		return nil, result, err
	}
	if result.Status != http.StatusOK {
		return nil, result, err
	}
	if len(frame) == 0 {
		return nil, result, errors.New("Ara guide-focus frame response was empty")
	}
	return frame, result, nil
}
