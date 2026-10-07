// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"uuid"
)

// ponytail: cap offsets at one billion to bound SQLite work; raise with verified retention-scale evidence.
const maxFaultOffset = 1_000_000_000

// Fault is one retained diagnostic row from Ara's fault log.
type Fault struct {
	ID             string     `json:"id"`
	SessionID      *string    `json:"session_id"`
	DetectedUTC    time.Time  `json:"detected_utc"`
	EquipmentType  string     `json:"equipment_type"`
	EquipmentID    *string    `json:"equipment_id"`
	EquipmentName  *string    `json:"equipment_name"`
	FaultType      string     `json:"fault_type"`
	Details        *string    `json:"details"`
	ActionTaken    *string    `json:"action_taken"`
	ResolvedUTC    *time.Time `json:"resolved_utc"`
	AffectedFrames []string   `json:"affected_frames"`
}

// FaultPage contains one page from Ara's retained fault history.
type FaultPage = Page[Fault]

// FaultListParams bounds the supported Ara fault-history query.
type FaultListParams struct {
	Limit          int
	Cursor         string
	EquipmentType  string
	SessionID      string
	UnresolvedOnly *bool
	FaultType      string
}

// ListFaultsWithRequestID reads one bounded page from Ara's retained fault log.
func (c *Client) ListFaultsWithRequestID(ctx context.Context, params FaultListParams, requestID string) (FaultPage, Result, error) {
	if len(params.Cursor) > 10 {
		return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault page limit or cursor exceeds the supported range")
	}
	if params.Limit == 0 {
		params.Limit = 50
	}
	if params.Limit < 1 || params.Limit > 200 {
		return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault page limit must be between 1 and 200")
	}
	if params.Cursor != "" {
		offset, err := strconv.ParseUint(params.Cursor, 10, 31)
		if err != nil || offset > maxFaultOffset || strconv.FormatUint(offset, 10) != params.Cursor {
			return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault cursor must be a bounded non-negative numeric offset")
		}
	}
	query := map[string][]string{"limit": {strconv.Itoa(params.Limit)}}
	if params.Cursor != "" {
		query["cursor"] = []string{params.Cursor}
	}
	if params.EquipmentType != "" {
		if len(params.EquipmentType) > 64 || !faultToken(params.EquipmentType) {
			return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault equipment type must be a bounded lowercase token")
		}
		query["equipmentType"] = []string{params.EquipmentType}
	}
	if params.SessionID != "" {
		id, err := uuid.Parse(params.SessionID)
		if err != nil || id.String() != strings.ToLower(params.SessionID) {
			return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault session ID must be a UUID")
		}
		query["sessionId"] = []string{id.String()}
	}
	if params.UnresolvedOnly != nil {
		query["unresolvedOnly"] = []string{strconv.FormatBool(*params.UnresolvedOnly)}
	}
	if params.FaultType != "" {
		if len(params.FaultType) > 64 || !faultToken(params.FaultType) {
			return FaultPage{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault type filter must be a bounded lowercase token")
		}
		query["faultType"] = []string{params.FaultType}
	}
	var page FaultPage
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/faults", Query: query, RequestID: requestID}, &page)
	if err == nil && page.NextCursor != nil {
		offset, cursorErr := strconv.ParseUint(*page.NextCursor, 10, 31)
		if cursorErr != nil || offset > maxFaultOffset || strconv.FormatUint(offset, 10) != *page.NextCursor {
			return FaultPage{}, Result{Outcome: OutcomeFailed}, &RequestError{Class: "decode", Outcome: OutcomeFailed, cause: errors.New("Ara fault page returned an invalid numeric cursor")}
		}
	}
	return page, result, err
}

// GetFaultWithRequestID reads one retained fault by its Ara UUID.
func (c *Client) GetFaultWithRequestID(ctx context.Context, id, requestID string) (Fault, Result, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != strings.ToLower(id) {
		return Fault{}, Result{Outcome: OutcomeFailed}, invalidFaultRequest("fault ID must be a UUID")
	}
	var fault Fault
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/faults/{id}", PathParams: map[string]string{"id": parsed.String()}, RequestID: requestID}, &fault)
	return fault, result, err
}

func faultToken(value string) bool {
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_' || char == '-') {
			return false
		}
	}
	return true
}

func invalidFaultRequest(message string) error {
	return &RequestError{Class: "invalid_request", Outcome: OutcomeFailed, cause: errors.New("ara request: " + message)}
}
