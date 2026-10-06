// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const maxPreviewBytes = 1 << 20

// Job is Ara's bounded status view for asynchronous jobs.
type Job struct {
	JobID       string     `json:"job_id"`
	JobType     string     `json:"job_type"`
	State       string     `json:"state"`
	Done        int        `json:"done"`
	Total       int        `json:"total"`
	StartedUTC  time.Time  `json:"started_utc"`
	FinishedUTC *time.Time `json:"finished_utc"`
	Error       string     `json:"error_message,omitempty"`
}

// FramePage contains one cursor page of lightweight frame records.
// FrameListItem is Ara's lightweight paged frame record.
type FrameListItem struct {
	ID                    string     `json:"id"`
	SessionID             string     `json:"session_id"`
	TargetName            string     `json:"target_name"`
	FrameType             string     `json:"frame_type"`
	FilterName            *string    `json:"filter_name,omitempty"`
	ExposureSeconds       float64    `json:"exposure_seconds"`
	CapturedUTC           time.Time  `json:"captured_utc"`
	HFR                   *float64   `json:"hfr,omitempty"`
	StarCount             *int       `json:"star_count,omitempty"`
	CompositeQualityScore *float64   `json:"composite_quality_score,omitempty"`
	Rating                int        `json:"rating"`
	SyncedAt              *time.Time `json:"synced_at,omitempty"`
	SyncTarget            *string    `json:"sync_target,omitempty"`
}

// Frame contains Ara's useful catalog metadata without exposing its server-local file path.
type Frame struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	TargetName       string    `json:"target_name"`
	FrameType        string    `json:"frame_type"`
	FilterName       *string   `json:"filter_name,omitempty"`
	ExposureSeconds  float64   `json:"exposure_seconds"`
	Gain             *int      `json:"gain,omitempty"`
	Offset           *int      `json:"offset,omitempty"`
	TemperatureC     *float64  `json:"temperature_c,omitempty"`
	CapturedUTC      time.Time `json:"captured_utc"`
	FileSizeBytes    int64     `json:"file_size_bytes"`
	Width            int       `json:"width"`
	Height           int       `json:"height"`
	BitDepth         int       `json:"bit_depth"`
	HFR              *float64  `json:"hfr,omitempty"`
	StarCount        *int      `json:"star_count,omitempty"`
	Eccentricity     *float64  `json:"eccentricity,omitempty"`
	GuidingRMSArcsec *float64  `json:"guiding_rms_arcsec,omitempty"`
	SNREstimate      *float64  `json:"snr_estimate,omitempty"`
	Rating           int       `json:"rating"`
	Tags             []string  `json:"tags"`
	FocuserPosition  *int      `json:"focuser_position,omitempty"`
}

// FramePage is one page of lightweight Ara frame records.
type FramePage = Page[FrameListItem]

// GetJobWithRequestID reads a job without treating a queued/running receipt as completion.
func (c *Client) GetJobWithRequestID(ctx context.Context, id, requestID string) (Job, Result, error) {
	var job Job
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/jobs/{id}", PathParams: map[string]string{"id": id}, RequestID: requestID}, &job)
	return job, result, err
}

// ListFramesWithRequestID reads one bounded page from Ara's frame catalog.
func (c *Client) ListFramesWithRequestID(ctx context.Context, limit int, cursor, requestID string) (FramePage, Result, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 {
		return FramePage{}, Result{Outcome: OutcomeFailed}, &RequestError{Class: "invalid_request", Outcome: OutcomeFailed, cause: errors.New("frame page limit or cursor exceeds the supported range")}
	}
	query := map[string][]string{"limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		query["cursor"] = []string{cursor}
	}
	var page FramePage
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/frames", Query: query, RequestID: requestID}, &page)
	return page, result, err
}

// GetFrameWithRequestID returns Ara's metadata for one catalogued frame.
func (c *Client) GetFrameWithRequestID(ctx context.Context, id, requestID string) (Frame, Result, error) {
	var frame Frame
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/frames/{id}", PathParams: map[string]string{"id": id}, RequestID: requestID}, &frame)
	return frame, result, err
}

// GetFrameThumbnailWithRequestID retrieves Ara's JPEG thumbnail, capped at one MiB.
func (c *Client) GetFrameThumbnailWithRequestID(ctx context.Context, id, requestID string) ([]byte, Result, error) {
	var image []byte
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/frames/{id}/thumbnail", PathParams: map[string]string{"id": id}, RequestID: requestID, RawResponse: true, MaxResponseBytes: maxPreviewBytes}, &image)
	return image, result, err
}
