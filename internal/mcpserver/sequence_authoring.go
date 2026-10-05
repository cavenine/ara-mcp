// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"uuid"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"
)

// SequenceValidationInput carries an opaque sequence tree to Ara's validator.
type SequenceValidationInput struct {
	Body jsontext.Value `json:"body"`
}

// SequenceValidation separates Ara's structural result from the local palette check.
type SequenceValidation struct {
	Valid               bool   `json:"valid"`
	Reason              string `json:"reason,omitempty"`
	ExecutableSupported bool   `json:"executable_supported"`
	SupportReason       string `json:"support_reason,omitempty"`
}

type sequenceCreateInput struct {
	ControlID      string         `json:"control_id"`
	IntentID       string         `json:"intent_id"`
	Name           string         `json:"name"`
	Description    *string        `json:"description,omitempty"`
	TemplateOrigin *string        `json:"template_origin,omitempty"`
	Body           jsontext.Value `json:"body"`
}

type sequenceUpdateInput struct {
	ControlID   string          `json:"control_id"`
	IntentID    string          `json:"intent_id"`
	SequenceID  string          `json:"sequence_id"`
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Body        *jsontext.Value `json:"body,omitempty"`
}

type sequenceTemplateInstantiateInput struct {
	ControlID       string         `json:"control_id"`
	IntentID        string         `json:"intent_id"`
	TemplateName    string         `json:"template_name"`
	NewSequenceName string         `json:"new_sequence_name"`
	Parameters      jsontext.Value `json:"parameters,omitempty"`
}

// SequenceMutationResult contains the saved sequence and adapter dispatch receipt.
type SequenceMutationResult struct {
	Sequence jsontext.Value  `json:"sequence"`
	Mutation MutationReceipt `json:"mutation"`
}

func registerSequenceAuthoringTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client) {
	tool := &mcp.Tool{
		Name:        "list_sequence_templates",
		Description: "List Ara sequence templates and their opaque sequence bodies. Built-in templates may be placeholders; inspect and validate before saving.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
		OutputSchema: &jsonschema.Schema{
			Type: "array",
			Items: &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{
				"name": {Type: "string"}, "category": {Type: "string"},
				"description": {Type: "string"}, "is_built_in": {Type: "boolean"},
				"body": {Type: "object", AdditionalProperties: &jsonschema.Schema{}},
			}},
		},
	}
	addTool(server, instrumentation, tool, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) ([]ara.SequenceTemplate, error) {
		templates, _, err := client.ListSequenceTemplatesWithRequestID(ctx, requestID(ctx))
		if err != nil {
			return nil, err
		}
		return templates, nil
	})
	validateTool := &mcp.Tool{
		Name:        "validate_sequence",
		Description: "Ask Ara to structurally validate an opaque sequence body and report whether its executable instructions fit this adapter's verified palette. Neither check confirms rig readiness.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{"body": sequenceBodySchema()}, "body"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}
	addTool(server, instrumentation, validateTool, func(ctx context.Context, request *mcp.CallToolRequest, input SequenceValidationInput) (SequenceValidation, error) {
		body, err := rawSequenceBody(request, input.Body)
		if err != nil {
			return SequenceValidation{}, fmt.Errorf("read sequence body: %w", err)
		}
		if !validSequenceBody(body) {
			return SequenceValidation{}, fmt.Errorf("%w: sequence body must be a JSON object", errInvalidArgument)
		}
		validation, _, err := client.ValidateSequenceWithRequestID(ctx, body, requestID(ctx))
		if err != nil {
			return SequenceValidation{}, fmt.Errorf("validate Ara sequence: %w", err)
		}
		result := SequenceValidation{Valid: validation.Valid, Reason: validation.Reason}
		if err := validateSupportedSequenceBody(body); err != nil {
			result.ExecutableSupported = false
			result.SupportReason = err.Error()
		} else {
			result.ExecutableSupported = true
		}
		return result, nil
	})
}

func registerSequenceMutationTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client, control *ControlManager) {
	createTool := &mcp.Tool{
		Name:        "create_sequence",
		Description: "Save a sequence body in Ara without starting it. Requires begin_control; Ara's validator and adapter palette checks do not confirm rig readiness.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"control_id":      stringSchema("Current control ID from begin_control"),
			"intent_id":       stringSchema("Stable ID for this logical create; reused IDs replay the same intent"),
			"name":            stringSchema("Saved sequence name"),
			"description":     stringSchema("Optional sequence description"),
			"template_origin": stringSchema("Optional source template name"),
			"body":            sequenceBodySchema(),
		}, "control_id", "intent_id", "name", "body"),
		OutputSchema: sequenceMutationSchema(),
		Annotations:  &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}
	addTool(server, instrumentation, createTool, func(ctx context.Context, request *mcp.CallToolRequest, input sequenceCreateInput) (SequenceMutationResult, error) {
		body, err := rawSequenceBody(request, input.Body)
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("read sequence body: %w", err)
		}
		input.Body = body
		name := strings.TrimSpace(input.Name)
		if name == "" || !validSequenceBody(input.Body) {
			return SequenceMutationResult{}, fmt.Errorf("%w: sequence name and object body are required", errInvalidArgument)
		}
		if err := validateSupportedSequenceBody(input.Body); err != nil {
			return SequenceMutationResult{}, fmt.Errorf("%w: %w", errInvalidArgument, err)
		}
		input.Name = name
		arguments, err := json.Marshal(struct {
			Name           string         `json:"name"`
			Description    *string        `json:"description,omitempty"`
			TemplateOrigin *string        `json:"template_origin,omitempty"`
			Body           jsontext.Value `json:"body"`
		}{input.Name, input.Description, input.TemplateOrigin, input.Body})
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("encode sequence create intent: %w", err)
		}
		var saved jsontext.Value
		receipt, err := control.DispatchMutation(ctx, MutationRequest{
			ControlID: input.ControlID, IntentID: input.IntentID, Operation: "create_sequence",
			Arguments: arguments, Kind: MutationNormal,
		}, func(ctx context.Context) (MutationReceipt, error) {
			created, result, dispatchErr := client.CreateSequenceWithRequestID(ctx, ara.SequenceCreate{
				Name: input.Name, Description: input.Description, TemplateOrigin: input.TemplateOrigin, Body: input.Body,
			}, input.IntentID, requestID(ctx))
			saved = created
			receipt := araMutationReceipt(result, dispatchErr)
			if dispatchErr == nil {
				var identityErr error
				receipt.SequenceID, identityErr = normalizeSequenceID(sequenceIDFromDetail(saved))
				if identityErr != nil {
					dispatchErr = fmt.Errorf("Ara create response omitted a valid sequence ID: %w", identityErr)
					receipt.Outcome = ara.OutcomeUnknown
					receipt.RetrySafe = false
					receipt.ErrorClass = "invalid_upstream_response"
				}
			}
			return receipt, dispatchErr
		})
		if err != nil {
			return SequenceMutationResult{}, sequenceMutationError("create sequence", receipt, err)
		}
		if len(saved) == 0 && receipt.Replayed {
			saved, err = readSavedSequence(ctx, client, receipt.SequenceID, requestID(ctx))
			if err != nil {
				return SequenceMutationResult{}, fmt.Errorf("read back replayed sequence: %w", err)
			}
		}
		logSavedSequence(ctx, instrumentation, "sequence_saved", receipt.SequenceID, receipt.Outcome)
		return SequenceMutationResult{Sequence: saved, Mutation: receipt}, nil
	})

	updateTool := &mcp.Tool{
		Name:        "update_sequence",
		Description: "Update selected fields of a saved Ara sequence. Ara rejects updates while that sequence has an active run; sequence IDs are UUIDs.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"control_id":  stringSchema("Current control ID from begin_control"),
			"intent_id":   stringSchema("Stable ID for this logical update"),
			"sequence_id": stringSchema("Ara sequence UUID"),
			"name":        stringSchema("Optional replacement name"),
			"description": stringSchema("Optional replacement description"),
			"body":        sequenceBodySchema(),
		}, "control_id", "intent_id", "sequence_id"),
		OutputSchema: sequenceMutationSchema(),
		Annotations:  &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}
	addTool(server, instrumentation, updateTool, func(ctx context.Context, request *mcp.CallToolRequest, input sequenceUpdateInput) (SequenceMutationResult, error) {
		body, err := rawSequenceBody(request, nil)
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("read sequence body: %w", err)
		}
		if len(body) > 0 {
			input.Body = &body
		}
		sequenceID, err := normalizeSequenceID(input.SequenceID)
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("%w: %v", errInvalidArgument, err)
		}
		if input.Name == nil && input.Description == nil && input.Body == nil {
			return SequenceMutationResult{}, fmt.Errorf("%w: at least one update field is required", errInvalidArgument)
		}
		if input.Name != nil {
			name := strings.TrimSpace(*input.Name)
			if name == "" {
				return SequenceMutationResult{}, fmt.Errorf("%w: sequence name must not be empty", errInvalidArgument)
			}
			input.Name = &name
		}
		if input.Body != nil {
			if !validSequenceBody(*input.Body) {
				return SequenceMutationResult{}, fmt.Errorf("%w: sequence body must be a JSON object", errInvalidArgument)
			}
			if err := validateSupportedSequenceBody(*input.Body); err != nil {
				return SequenceMutationResult{}, fmt.Errorf("%w: %w", errInvalidArgument, err)
			}
		}
		arguments, err := json.Marshal(struct {
			SequenceID  string          `json:"sequence_id"`
			Name        *string         `json:"name,omitempty"`
			Description *string         `json:"description,omitempty"`
			Body        *jsontext.Value `json:"body,omitempty"`
		}{sequenceID, input.Name, input.Description, input.Body})
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("encode sequence update intent: %w", err)
		}
		var saved jsontext.Value
		receipt, err := control.DispatchMutation(ctx, MutationRequest{
			ControlID: input.ControlID, IntentID: input.IntentID, Operation: "update_sequence",
			Arguments: arguments, Kind: MutationNormal,
		}, func(ctx context.Context) (MutationReceipt, error) {
			updated, result, dispatchErr := client.UpdateSequenceWithRequestID(ctx, sequenceID, ara.SequenceUpdate{
				Name: input.Name, Description: input.Description, Body: input.Body,
			}, requestID(ctx))
			saved = updated
			receipt := araMutationReceipt(result, dispatchErr)
			receipt.SequenceID = sequenceID
			if dispatchErr == nil && sequenceIDFromDetail(saved) != sequenceID {
				dispatchErr = errors.New("Ara update response omitted or changed the sequence ID")
				receipt.Outcome = ara.OutcomeUnknown
				receipt.RetrySafe = false
				receipt.ErrorClass = "invalid_upstream_response"
			}
			return receipt, dispatchErr
		})
		if err != nil {
			return SequenceMutationResult{}, sequenceMutationError("update sequence", receipt, err)
		}
		if len(saved) == 0 && receipt.Replayed {
			saved, err = readSavedSequence(ctx, client, receipt.SequenceID, requestID(ctx))
			if err != nil {
				return SequenceMutationResult{}, fmt.Errorf("read back replayed sequence: %w", err)
			}
		}
		logSavedSequence(ctx, instrumentation, "sequence_updated", sequenceID, receipt.Outcome)
		return SequenceMutationResult{Sequence: saved, Mutation: receipt}, nil
	})

	instantiateTool := &mcp.Tool{
		Name:        "instantiate_sequence_template",
		Description: "Instantiate an Ara template and save the resulting sequence without starting it. Requires begin_control; template names must come from list_sequence_templates.",
		InputSchema: objectSchema(map[string]*jsonschema.Schema{
			"control_id":        stringSchema("Current control ID from begin_control"),
			"intent_id":         stringSchema("Stable ID for this logical instantiation"),
			"template_name":     stringSchema("Name returned by list_sequence_templates"),
			"new_sequence_name": stringSchema("Name for the saved sequence"),
			"parameters":        sequenceBodySchema(),
		}, "control_id", "intent_id", "template_name", "new_sequence_name"),
		OutputSchema: sequenceMutationSchema(),
		Annotations:  &mcp.ToolAnnotations{OpenWorldHint: new(false)},
	}
	addTool(server, instrumentation, instantiateTool, func(ctx context.Context, request *mcp.CallToolRequest, input sequenceTemplateInstantiateInput) (SequenceMutationResult, error) {
		parameters, err := rawJSONArgument(request, "parameters", input.Parameters)
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("read template parameters: %w", err)
		}
		input.Parameters = parameters
		input.TemplateName = strings.TrimSpace(input.TemplateName)
		input.NewSequenceName = strings.TrimSpace(input.NewSequenceName)
		if input.TemplateName == "" || input.NewSequenceName == "" || (len(parameters) > 0 && !validSequenceBody(parameters)) {
			return SequenceMutationResult{}, fmt.Errorf("%w: template name, new sequence name, and an object parameter set are required", errInvalidArgument)
		}
		arguments, err := json.Marshal(struct {
			TemplateName    string         `json:"template_name"`
			NewSequenceName string         `json:"new_sequence_name"`
			Parameters      jsontext.Value `json:"parameters,omitempty"`
		}{input.TemplateName, input.NewSequenceName, input.Parameters})
		if err != nil {
			return SequenceMutationResult{}, fmt.Errorf("encode template instantiation intent: %w", err)
		}
		var saved jsontext.Value
		receipt, err := control.DispatchMutation(ctx, MutationRequest{
			ControlID: input.ControlID, IntentID: input.IntentID, Operation: "instantiate_sequence_template",
			Arguments: arguments, Kind: MutationNormal,
		}, func(ctx context.Context) (MutationReceipt, error) {
			instantiated, result, dispatchErr := client.InstantiateSequenceTemplateWithRequestID(ctx, input.TemplateName, ara.TemplateInstantiation{
				NewSequenceName: input.NewSequenceName, Parameters: input.Parameters,
			}, requestID(ctx))
			saved = instantiated
			receipt := araMutationReceipt(result, dispatchErr)
			if dispatchErr == nil {
				var identityErr error
				receipt.SequenceID, identityErr = normalizeSequenceID(sequenceIDFromDetail(saved))
				if identityErr != nil {
					dispatchErr = fmt.Errorf("Ara template response omitted a valid sequence ID: %w", identityErr)
					receipt.Outcome = ara.OutcomeUnknown
					receipt.RetrySafe = false
					receipt.ErrorClass = "invalid_upstream_response"
				}
			}
			return receipt, dispatchErr
		})
		if err != nil {
			return SequenceMutationResult{}, sequenceMutationError("instantiate sequence template", receipt, err)
		}
		if len(saved) == 0 && receipt.Replayed {
			saved, err = readSavedSequence(ctx, client, receipt.SequenceID, requestID(ctx))
			if err != nil {
				return SequenceMutationResult{}, fmt.Errorf("read back replayed sequence: %w", err)
			}
		}
		logSavedSequence(ctx, instrumentation, "sequence_saved", receipt.SequenceID, receipt.Outcome)
		return SequenceMutationResult{Sequence: saved, Mutation: receipt}, nil
	})
}

func objectSchema(properties map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Properties: properties, Required: required}
}

func stringSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "string", Description: description}
}

func sequenceBodySchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", Description: "Opaque Ara sequence object; unknown fields and $type values are preserved", AdditionalProperties: &jsonschema.Schema{}}
}

func sequenceMutationSchema() *jsonschema.Schema {
	return objectSchema(map[string]*jsonschema.Schema{
		"sequence": sequenceBodySchema(),
		"mutation": objectSchema(map[string]*jsonschema.Schema{
			"outcome":     stringSchema("Ara dispatch outcome"),
			"receipt_id":  stringSchema("Ara operation receipt ID when supplied"),
			"sequence_id": stringSchema("Saved Ara sequence ID"),
			"retry_safe":  {Type: "boolean"},
			"replayed":    {Type: "boolean"},
		}),
	}, "sequence", "mutation")
}

func validSequenceBody(body jsontext.Value) bool {
	return len(body) > 0 && body.IsValid() && body.Kind() == '{'
}

func rawSequenceBody(request *mcp.CallToolRequest, fallback jsontext.Value) (jsontext.Value, error) {
	return rawJSONArgument(request, "body", fallback)
}

func rawJSONArgument(request *mcp.CallToolRequest, name string, fallback jsontext.Value) (jsontext.Value, error) {
	if request == nil || request.Params == nil || len(request.Params.Arguments) == 0 {
		return fallback, nil
	}
	var arguments map[string]jsontext.Value
	if err := json.Unmarshal(request.Params.Arguments, &arguments); err != nil {
		return nil, err
	}
	value := arguments[name]
	if len(value) == 0 {
		return fallback, nil
	}
	return value, nil
}

func araMutationReceipt(result ara.Result, err error) MutationReceipt {
	receipt := MutationReceipt{Outcome: result.Outcome, ReceiptID: result.ReceiptID, RetrySafe: result.RetrySafe}
	if err != nil {
		receipt.ErrorClass = toolErrorClass(err)
	}
	return receipt
}

func sequenceMutationError(operation string, receipt MutationReceipt, err error) error {
	if errors.Is(err, errIntentConflict) {
		return fmt.Errorf("%w: %s intent ID has already been used with different arguments", errIntentConflict, operation)
	}
	switch receipt.ErrorClass {
	case "upstream_conflict":
		if apiError, ok := errors.AsType[*ara.APIError](err); ok {
			return fmt.Errorf("Ara rejected %s because of a conflict: %s: %w", operation, apiError.Error(), err)
		}
		return fmt.Errorf("Ara rejected %s because the sequence may have an active run: %w", operation, err)
	case "not_found":
		if apiError, ok := errors.AsType[*ara.APIError](err); ok {
			return fmt.Errorf("Ara could not find the sequence or template for %s: %s: %w", operation, apiError.Error(), err)
		}
		return fmt.Errorf("Ara could not find the sequence or template for %s: %w", operation, err)
	case "invalid_upstream_request":
		if apiError, ok := errors.AsType[*ara.APIError](err); ok {
			return fmt.Errorf("Ara rejected %s as invalid: %s: %w", operation, apiError.Error(), err)
		}
		return fmt.Errorf("Ara rejected %s as invalid: %w", operation, err)
	case "timeout", "network", "upstream_unavailable":
		if receipt.Outcome == ara.OutcomeUnknown {
			return fmt.Errorf("%s outcome is unknown; reconcile in Ara before retrying: %w", operation, err)
		}
	}
	return err
}

func sequenceIDFromDetail(detail jsontext.Value) string {
	var identity struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(detail, &identity); err != nil {
		return ""
	}
	return identity.ID
}

func readSavedSequence(ctx context.Context, client *ara.Client, id, requestID string) (jsontext.Value, error) {
	canonical, err := normalizeSequenceID(id)
	if err != nil {
		return nil, fmt.Errorf("saved sequence ID is invalid: %w", err)
	}
	detail, _, err := client.GetSequenceWithRequestID(ctx, canonical, requestID)
	if err != nil {
		return nil, fmt.Errorf("read saved Ara sequence: %w", err)
	}
	return detail, nil
}

func normalizeSequenceID(id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func logSavedSequence(ctx context.Context, instrumentation toolInstrumentation, event, sequenceID string, outcome ara.Outcome) {
	fields := []any{
		"service", "ara-mcp", "version", instrumentation.version,
		"component", "sequence", "event", event, "transport", instrumentation.transport,
		"request_id", requestID(ctx), "sequence_id", sequenceID, "outcome", outcome,
	}
	if span := trace.SpanFromContext(ctx).SpanContext(); span.IsValid() {
		fields = append(fields, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String())
	}
	instrumentation.logger.InfoContext(ctx, "Ara saved sequence changed", fields...)
}
