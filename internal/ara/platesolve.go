// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/v2"
	"errors"
	"math"
	"net/http"
	"uuid"
)

// PlateSolveResult is Ara's bounded astrometric solution; no image data or paths are returned.
type PlateSolveResult struct {
	Success      bool     `json:"success"`
	RA           *float64 `json:"ra"`
	Dec          *float64 `json:"dec"`
	Orientation  *float64 `json:"orientation"`
	PixelScale   *float64 `json:"pixel_scale"`
	SearchRadius *float64 `json:"search_radius"`
}

// SolveFrameWithRequestID solves a catalogued frame in Ara.
func (c *Client) SolveFrameWithRequestID(ctx context.Context, id string, approximate *[2]float64, requestID string) (PlateSolveResult, Result, error) {
	frameID, err := uuid.Parse(id)
	if err != nil || frameID.String() != id {
		return PlateSolveResult{}, Result{Outcome: OutcomeFailed}, invalidPlateSolveRequest("frame ID must be a UUID")
	}
	body := []byte("{}")
	if approximate != nil {
		if math.IsNaN(approximate[0]) || math.IsInf(approximate[0], 0) || math.IsNaN(approximate[1]) || math.IsInf(approximate[1], 0) || approximate[0] < 0 || approximate[0] >= 24 || approximate[1] < -90 || approximate[1] > 90 {
			return PlateSolveResult{}, Result{Outcome: OutcomeFailed}, invalidPlateSolveRequest("coordinate hint is out of range")
		}
		var err error
		body, err = json.Marshal(struct {
			ApproxRA  float64 `json:"approx_ra_hours"`
			ApproxDec float64 `json:"approx_dec_degrees"`
		}{approximate[0], approximate[1]})
		if err != nil {
			return PlateSolveResult{}, Result{Outcome: OutcomeFailed}, errors.New("encode plate-solve coordinate hint")
		}
	}
	var solution PlateSolveResult
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/platesolve/frames/{id}/solve", PathParams: map[string]string{"id": frameID.String()}, Body: body, RequestID: requestID}, &solution)
	return solution, result, err
}

// CenterWithRequestID enqueues Ara's coordinate-centering job.
func (c *Client) CenterWithRequestID(ctx context.Context, raHours, decDegrees float64, requestID string) (BatchJob, Result, error) {
	if math.IsNaN(raHours) || math.IsInf(raHours, 0) || math.IsNaN(decDegrees) || math.IsInf(decDegrees, 0) || raHours < 0 || raHours >= 24 || decDegrees < -90 || decDegrees > 90 {
		return BatchJob{}, Result{Outcome: OutcomeFailed}, invalidPlateSolveRequest("center coordinates are out of range")
	}
	body, err := json.Marshal(struct {
		RAHours    float64 `json:"ra_hours"`
		DecDegrees float64 `json:"dec_degrees"`
	}{raHours, decDegrees})
	if err != nil {
		return BatchJob{}, Result{Outcome: OutcomeFailed}, err
	}
	var job BatchJob
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/platesolve/center", Body: body, RequestID: requestID}, &job)
	return job, result, err
}

// CancelJobWithRequestID requests cancellation of an Ara background job.
func (c *Client) CancelJobWithRequestID(ctx context.Context, id, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodDelete, Route: "/jobs/{id}", PathParams: map[string]string{"id": id}, RequestID: requestID}, nil)
}

func invalidPlateSolveRequest(message string) error {
	return &RequestError{Class: "invalid_request", Outcome: OutcomeFailed, cause: errors.New("ara request: " + message)}
}
