// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// FaultListInput bounds a page of Ara's retained fault history.
type FaultListInput struct {
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum 200; defaults to 50"`
	Cursor         string `json:"cursor,omitempty" jsonschema:"Numeric Ara offset cursor returned as next_cursor"`
	EquipmentType  string `json:"equipment_type,omitempty" jsonschema:"Optional lowercase Ara equipment type token"`
	SessionID      string `json:"session_id,omitempty" jsonschema:"Filter by Ara session UUID"`
	UnresolvedOnly *bool  `json:"unresolved_only,omitempty"`
	FaultType      string `json:"fault_type,omitempty" jsonschema:"Filter by bounded lowercase Ara fault type token"`
}

// FaultInput identifies one retained Ara fault.
type FaultInput struct {
	FaultID string `json:"fault_id" jsonschema:"Ara fault UUID"`
}

func registerFaultTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client) {
	addTool(server, instrumentation, &mcp.Tool{
		Name: "list_faults", Description: "Read a bounded page of Ara's retained fault history. Cursors are numeric offsets; retention may prune older rows, so this is not a complete event journal or current device state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FaultListInput) (ara.FaultPage, error) {
		page, _, err := client.ListFaultsWithRequestID(ctx, ara.FaultListParams{
			Limit: input.Limit, Cursor: input.Cursor, EquipmentType: input.EquipmentType,
			SessionID: input.SessionID, UnresolvedOnly: input.UnresolvedOnly, FaultType: input.FaultType,
		}, requestID(ctx))
		return page, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_fault", Description: "Read one retained Ara fault record by its fault UUID.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FaultInput) (ara.Fault, error) {
		fault, _, err := client.GetFaultWithRequestID(ctx, input.FaultID, requestID(ctx))
		return fault, err
	})
}
