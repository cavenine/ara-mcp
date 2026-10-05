// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
)

// SequenceTemplate is a listed Ara template with its opaque body preserved.
type SequenceTemplate struct {
	Name        string         `json:"name"`
	Category    string         `json:"category"`
	Description string         `json:"description,omitempty"`
	IsBuiltIn   bool           `json:"is_built_in"`
	Body        jsontext.Value `json:"body"`
}

// SequenceValidation is Ara's structural sequence-body validation result.
type SequenceValidation struct {
	Valid  bool   `json:"valid"`
	Reason string `json:"reason,omitempty"`
}

// SequenceCreate is the request body for saving a new sequence.
type SequenceCreate struct {
	Name           string         `json:"name"`
	Description    *string        `json:"description,omitempty"`
	TemplateOrigin *string        `json:"template_origin,omitempty"`
	Body           jsontext.Value `json:"body"`
}

// SequenceUpdate is a partial request body for editing a saved sequence.
type SequenceUpdate struct {
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Body        *jsontext.Value `json:"body,omitempty"`
}

// TemplateInstantiation requests that Ara instantiate and save a template.
type TemplateInstantiation struct {
	NewSequenceName string         `json:"new_sequence_name"`
	Parameters      jsontext.Value `json:"parameters,omitempty"`
}

// GetSequenceWithRequestID reads one saved sequence and preserves its opaque body.
func (c *Client) GetSequenceWithRequestID(ctx context.Context, id, requestID string) (jsontext.Value, Result, error) {
	var response jsontext.Value
	result, err := c.do(ctx, request{
		Method: http.MethodGet, Route: "/sequences/{id}",
		PathParams: map[string]string{"id": id}, RequestID: requestID,
	}, &response)
	return response, result, err
}

// ListSequenceTemplatesWithRequestID lists Ara's sequence templates.
func (c *Client) ListSequenceTemplatesWithRequestID(ctx context.Context, requestID string) ([]SequenceTemplate, Result, error) {
	var response []SequenceTemplate
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/sequences/templates", RequestID: requestID}, &response)
	return response, result, err
}

// ValidateSequenceWithRequestID asks Ara to structurally validate a raw sequence body.
func (c *Client) ValidateSequenceWithRequestID(ctx context.Context, body jsontext.Value, requestID string) (SequenceValidation, Result, error) {
	requestBody, err := json.Marshal(struct {
		Body jsontext.Value `json:"body"`
	}{body})
	if err != nil {
		return SequenceValidation{}, Result{Outcome: OutcomeFailed}, err
	}
	var response SequenceValidation
	result, err := c.do(ctx, request{Method: http.MethodPost, Route: "/sequences/validate", Body: requestBody, RequestID: requestID}, &response)
	return response, result, err
}

// CreateSequenceWithRequestID saves a sequence, optionally using Ara's create idempotency key.
func (c *Client) CreateSequenceWithRequestID(ctx context.Context, input SequenceCreate, idempotencyKey, requestID string) (jsontext.Value, Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, Result{Outcome: OutcomeFailed}, err
	}
	var response jsontext.Value
	result, err := c.do(ctx, request{
		Method: http.MethodPost, Route: "/sequences", Body: body,
		IdempotencyKey: idempotencyKey, RequestID: requestID,
	}, &response)
	return response, result, err
}

// UpdateSequenceWithRequestID applies a partial update to one saved sequence.
func (c *Client) UpdateSequenceWithRequestID(ctx context.Context, id string, input SequenceUpdate, requestID string) (jsontext.Value, Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, Result{Outcome: OutcomeFailed}, err
	}
	var response jsontext.Value
	result, err := c.do(ctx, request{
		Method: http.MethodPatch, Route: "/sequences/{id}",
		PathParams: map[string]string{"id": id}, Body: body, RequestID: requestID,
	}, &response)
	return response, result, err
}

// InstantiateSequenceTemplateWithRequestID asks Ara to substitute and save a template.
func (c *Client) InstantiateSequenceTemplateWithRequestID(ctx context.Context, templateName string, input TemplateInstantiation, requestID string) (jsontext.Value, Result, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, Result{Outcome: OutcomeFailed}, err
	}
	var response jsontext.Value
	result, err := c.do(ctx, request{
		Method: http.MethodPost, Route: "/sequences/templates/{name}/instantiate",
		PathParams: map[string]string{"name": templateName}, Body: body, RequestID: requestID,
	}, &response)
	return response, result, err
}
