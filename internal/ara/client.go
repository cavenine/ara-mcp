// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ara implements the Ara API boundary.
package ara

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultTimeout          = 10 * time.Second
	defaultMaxResponseBytes = 2 << 20
	maxReadRetries          = 2
	defaultSequenceLimit    = 50
	maxSequenceLimit        = 100
)

// Config configures the shared Ara connection and outgoing request instrumentation.
type Config struct {
	BaseURL          string
	Timeout          time.Duration
	MaxResponseBytes int64
	ReadRetries      int // zero disables retries; only GET requests are eligible
	Meter            metric.Meter
	Tracer           trace.Tracer
	Propagator       propagation.TextMapPropagator
}

// Client calls Ara through its HTTP gateway.
type Client struct{ gateway *httpGateway }

// Request describes an Ara route. Route is a template such as
// "/sequences/{id}"; PathParams supplies values for its placeholders.
type Request struct {
	Method         string
	Route          string
	PathParams     map[string]string
	Query          map[string][]string
	Body           []byte
	IdempotencyKey string
	RequestID      string
}

// Page is Ara's cursor-page wire envelope. Cursors remain opaque to the client.
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
	HasMore    bool    `json:"has_more"`
}

// Outcome describes the strongest operation evidence present in Ara's response.
type Outcome string

const (
	OutcomeFailed    Outcome = "failed"
	OutcomeCompleted Outcome = "completed"
	OutcomeAccepted  Outcome = "accepted"
	OutcomeUnknown   Outcome = "unknown"
)

// Result contains response evidence and retry classification, not inferred device state.
type Result struct {
	Outcome   Outcome
	Status    int
	RequestID string
	ReceiptID string
	RetrySafe bool
}

// ControlSession contains Ara's session capability. SessionID must remain private
// to the adapter and must never be returned to an MCP caller or written to logs.
type ControlSession struct {
	sessionID   string
	Hostname    string    `json:"hostname"`
	ConnectedAt time.Time `json:"connected_at"`
}

// SessionID returns the Ara capability for session-bound requests. Do not expose
// it in MCP results, logs, or diagnostics.
func (s ControlSession) SessionID() string { return s.sessionID }

// New validates configuration and creates the shared Ara client.
func New(config Config) (*Client, error) {
	if config.Timeout == 0 {
		config.Timeout = defaultTimeout
	}
	if config.Timeout < 0 {
		return nil, errors.New("ara client: timeout must be positive")
	}
	if config.MaxResponseBytes == 0 {
		config.MaxResponseBytes = defaultMaxResponseBytes
	}
	if config.MaxResponseBytes < 1 || config.MaxResponseBytes > defaultMaxResponseBytes {
		return nil, fmt.Errorf("ara client: response limit must be between 1 and %d bytes", defaultMaxResponseBytes)
	}
	if config.ReadRetries < 0 || config.ReadRetries > maxReadRetries {
		return nil, fmt.Errorf("ara client: read retries must be between 0 and %d", maxReadRetries)
	}
	gateway, err := newHTTPGateway(config)
	if err != nil {
		return nil, err
	}
	return &Client{gateway: gateway}, nil
}

// Do sends one Ara request. Mutations are never retried by the client.
func (c *Client) Do(ctx context.Context, request Request, response any) (Result, error) {
	if ctx == nil {
		return Result{Outcome: OutcomeFailed}, errors.New("ara request: context is required")
	}
	exchange, err := c.gateway.do(ctx, request, response)
	return exchange.result, err
}

// ConnectWithRequestID claims or reclaims Ara's single-client control slot.
// Passing a nil sessionID requests a fresh claim; passing the previous capability
// reclaims that same session after a network interruption.
func (c *Client) ConnectWithRequestID(ctx context.Context, hostname string, sessionID *string, requestID string) (ControlSession, Result, error) {
	if strings.TrimSpace(hostname) == "" || (sessionID != nil && *sessionID == "") {
		return ControlSession{}, Result{Outcome: OutcomeFailed}, errors.New("ara connect: hostname and any prior session ID must be non-empty")
	}
	body, err := json.Marshal(struct {
		Hostname  string  `json:"hostname"`
		SessionID *string `json:"session_id"`
	}{Hostname: hostname, SessionID: sessionID})
	if err != nil {
		return ControlSession{}, Result{Outcome: OutcomeFailed}, fmt.Errorf("encode Ara connect request: %w", err)
	}
	var response struct {
		SessionID   string    `json:"session_id"`
		Hostname    string    `json:"hostname"`
		ConnectedAt time.Time `json:"connected_at"`
	}
	result, err := c.Do(ctx, Request{
		Method: http.MethodPost, Route: "/server/connect", Body: body, RequestID: requestID,
	}, &response)
	if err != nil {
		return ControlSession{}, result, err
	}
	if response.SessionID == "" {
		result.Outcome = OutcomeUnknown
		return ControlSession{}, result, errors.New("ara connect: response omitted session capability")
	}
	return ControlSession{sessionID: response.SessionID, Hostname: response.Hostname, ConnectedAt: response.ConnectedAt}, result, nil
}

// ListSequences requests the bounded sequence list supported by current Ara.
func (c *Client) ListSequences(ctx context.Context, limit int) (Page[jsontext.Value], Result, error) {
	return c.ListSequencesWithRequestID(ctx, limit, "")
}

// ListSequencesWithRequestID requests the bounded current-Ara sequence list and
// propagates logical request correlation to the gateway.
func (c *Client) ListSequencesWithRequestID(ctx context.Context, limit int, requestID string) (Page[jsontext.Value], Result, error) {
	if limit == 0 {
		limit = defaultSequenceLimit
	}
	if limit < 1 || limit > maxSequenceLimit {
		return Page[jsontext.Value]{}, Result{Outcome: OutcomeFailed}, errors.New("ara request: invalid sequence page parameters")
	}
	var page Page[jsontext.Value]
	result, err := c.Do(ctx, Request{
		Method:    httpMethodGet,
		Route:     "/sequences",
		Query:     map[string][]string{"limit": {fmt.Sprint(limit)}},
		RequestID: requestID,
	}, &page)
	return page, result, err
}
