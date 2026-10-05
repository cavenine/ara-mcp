// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package ara implements the Ara API boundary.
package ara

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
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
