// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// JobInput identifies one Ara background job.
type JobInput struct {
	JobID string `json:"job_id" jsonschema:"Ara job identifier"`
}

// FrameListInput bounds a cursor page of Ara frame metadata.
type FrameListInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"Maximum 100; defaults to 50"`
	Cursor string `json:"cursor,omitempty" jsonschema:"Opaque Ara frame cursor"`
}

// FrameInput identifies one catalogued frame.
type FrameInput struct {
	FrameID string `json:"frame_id" jsonschema:"Ara frame identifier"`
}

func registerProgressTools(server *mcp.Server, instrumentation toolInstrumentation, client *ara.Client) {
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_job_status", Description: "Read Ara's current queued, running, complete, failed, or cancelled job state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input JobInput) (ara.Job, error) {
		job, _, err := client.GetJobWithRequestID(ctx, input.JobID, requestID(ctx))
		return job, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "list_frames", Description: "Read a bounded cursor page of Ara's captured frame catalog.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FrameListInput) (ara.FramePage, error) {
		page, _, err := client.ListFramesWithRequestID(ctx, input.Limit, input.Cursor, requestID(ctx))
		return page, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name: "get_frame", Description: "Read metadata for one Ara catalogued frame; FITS files are not returned.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input FrameInput) (ara.Frame, error) {
		frame, _, err := client.GetFrameWithRequestID(ctx, input.FrameID, requestID(ctx))
		return frame, err
	})
	tool := &mcp.Tool{Name: "get_frame_preview", Description: "Read Ara's JPEG frame thumbnail as MCP image content (maximum 1 MiB).", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}}
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, input FrameInput) (*mcp.CallToolResult, any, error) {
		id, err := newRequestID()
		if err != nil {
			return nil, nil, fmt.Errorf("assign tool request id: %w", err)
		}
		ctx = context.WithValue(ctx, requestIDKey{}, id)
		started := time.Now()
		ctx, span := instrumentation.tracer.Start(ctx, "mcp.tool.get_frame_preview", trace.WithAttributes(attribute.String("mcp.tool.name", tool.Name)))
		defer span.End()
		attrs := attribute.String("mcp.tool.name", tool.Name)
		instrumentation.metrics.inflight.Add(ctx, 1, metric.WithAttributes(attrs))
		defer instrumentation.metrics.inflight.Add(ctx, -1, metric.WithAttributes(attrs))
		var image []byte
		var callErr error
		select {
		case instrumentation.reads <- struct{}{}:
			defer func() { <-instrumentation.reads }()
		default:
			callErr = errReadCapacity
		}
		if callErr == nil {
			image, _, callErr = client.GetFrameThumbnailWithRequestID(ctx, input.FrameID, requestID(ctx))
		}
		outcome, level := "success", slog.LevelInfo
		if callErr != nil {
			outcome, level = "error", slog.LevelError
			span.RecordError(errors.New("frame preview failed"))
			span.SetStatus(codes.Error, "frame preview failed")
		}
		fields := []any{"service", "ara-mcp", "version", instrumentation.version, "component", "tools", "event", "tool_call", "transport", instrumentation.transport, "request_id", id, "tool", tool.Name, "outcome", outcome, "duration_seconds", time.Since(started).Seconds()}
		if callErr != nil {
			fields = append(fields, "error_class", toolErrorClass(callErr))
		}
		if spanContext := span.SpanContext(); spanContext.IsValid() {
			fields = append(fields, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
		}
		instrumentation.logger.Log(ctx, level, "tool call completed", fields...)
		callAttrs := metric.WithAttributes(attribute.String("mcp.tool.name", tool.Name), attribute.String("mcp.transport", instrumentation.transport), attribute.String("mcp.outcome", outcome))
		instrumentation.metrics.calls.Add(ctx, 1, callAttrs)
		instrumentation.metrics.duration.Record(ctx, time.Since(started).Seconds(), callAttrs)
		if callErr != nil {
			return nil, nil, callErr
		}
		result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: image, MIMEType: "image/jpeg"}}}
		return result, map[string]any{"frame_id": input.FrameID, "mime_type": "image/jpeg", "size_bytes": len(image)}, nil
	})
}
