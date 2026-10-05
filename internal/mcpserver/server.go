// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mcpserver registers Ara read tools on the official MCP SDK server.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Options configures the shared MCP tool server.
type Options struct {
	Ara       *ara.Client
	Version   string
	Logger    *slog.Logger
	Meter     metric.Meter
	Tracer    trace.Tracer
	Transport string
	Sampler   *monitor.Sampler
	StartedAt time.Time
}

// New constructs an MCP server exposing the initial read-only Ara tools.
func New(options Options) (*mcp.Server, error) {
	if options.Ara == nil {
		return nil, errors.New("Ara client is required")
	}
	version := options.Version
	if version == "" {
		version = "dev"
	}
	options.Version = version
	if options.Logger == nil {
		options.Logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if options.Meter == nil {
		options.Meter = metricnoop.NewMeterProvider().Meter("github.com/cavenine/ara-mcp/internal/mcpserver")
	}
	if options.Tracer == nil {
		options.Tracer = tracenoop.NewTracerProvider().Tracer("github.com/cavenine/ara-mcp/internal/mcpserver")
	}
	if options.Transport == "" {
		options.Transport = "stdio"
	}
	metrics, err := newMetrics(options.Meter)
	if err != nil {
		return nil, err
	}
	instrumentation := toolInstrumentation{
		version: version, transport: options.Transport, logger: options.Logger,
		tracer: options.Tracer, metrics: metrics, reads: make(chan struct{}, 4),
	}
	if options.Sampler == nil {
		options.Sampler, err = monitor.NewSamplerWithMeter(options.Meter)
		if err != nil {
			return nil, fmt.Errorf("create process sampler: %w", err)
		}
	}
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now()
	}
	health := &araHealth{}
	sdkBaseLogger := options.Logger.With("service", "ara-mcp", "version", version, "component", "mcp")
	sdkLogger := slog.New(sdkLogHandler{Handler: sdkBaseLogger.Handler()})
	server := mcp.NewServer(&mcp.Implementation{Name: "ara-mcp", Version: version}, &mcp.ServerOptions{Logger: sdkLogger})
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "get_server_context",
		Description: "Read Ara server identity, API versions, and state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (ServerContext, error) {
		result, err := getServerContext(ctx, options.Ara)
		return result, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "get_rig_context",
		Description: "Read Ara's active profile, imaging defaults, filters, and connected device status.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (RigContext, error) {
		result, err := getRigContext(ctx, options.Ara)
		return result, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "list_sequences",
		Description: "List a bounded page of saved Ara sequences and their current run states.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SequenceListInput) (any, error) {
		result, err := listSequences(ctx, options.Ara, input.Limit)
		return result, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "get_sequence",
		Description: "Read a saved Ara sequence, preserving its opaque body.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input SequenceInput) (any, error) {
		result, err := getSequence(ctx, options.Ara, input.SequenceID)
		return result, err
	})
	addTool(server, instrumentation, &mcp.Tool{
		Name:        "get_adapter_diagnostics",
		Description: "Read local adapter health and resource diagnostics without changing Ara state.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (AdapterDiagnostics, error) {
		return getAdapterDiagnostics(ctx, options, health)
	})
	return server, nil
}

type requestIDKey struct{}

type toolMetrics struct {
	calls    metric.Int64Counter
	duration metric.Float64Histogram
	inflight metric.Int64UpDownCounter
}

type toolInstrumentation struct {
	version   string
	transport string
	logger    *slog.Logger
	tracer    trace.Tracer
	metrics   toolMetrics
	reads     chan struct{}
}

var errReadCapacity = errors.New("read tool capacity exhausted")
var errInvalidArgument = errors.New("invalid tool arguments")

type sdkLogHandler struct{ slog.Handler }

func (h sdkLogHandler) Handle(ctx context.Context, record slog.Record) error {
	// The SDK currently logs expected Run(ctx cancellation) at ERROR.
	if record.Message == "server run cancelled" {
		record.Message = "server run stopped"
		record.Level = slog.LevelInfo
		if !h.Enabled(ctx, record.Level) {
			return nil
		}
	}
	sanitized := slog.NewRecord(record.Time, record.Level, record.Message, record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "error" {
			attr = slog.String("error_class", "sdk_internal")
		}
		sanitized.AddAttrs(attr)
		return true
	})
	record = sanitized
	record.AddAttrs(slog.String("event", record.Message))
	return h.Handler.Handle(ctx, record)
}

func (h sdkLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return sdkLogHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h sdkLogHandler) WithGroup(name string) slog.Handler {
	return sdkLogHandler{Handler: h.Handler.WithGroup(name)}
}

func newMetrics(meter metric.Meter) (toolMetrics, error) {
	var result toolMetrics
	var err error
	if result.calls, err = meter.Int64Counter("mcp.tool.calls", metric.WithUnit("{call}"), metric.WithDescription("MCP tool calls by bounded tool and outcome")); err != nil {
		return toolMetrics{}, fmt.Errorf("create MCP call counter: %w", err)
	}
	if result.duration, err = meter.Float64Histogram("mcp.tool.duration", metric.WithUnit("s"), metric.WithDescription("MCP tool handler duration")); err != nil {
		return toolMetrics{}, fmt.Errorf("create MCP tool duration histogram: %w", err)
	}
	if result.inflight, err = meter.Int64UpDownCounter("mcp.tool.in_flight", metric.WithUnit("{call}"), metric.WithDescription("MCP tool calls currently executing")); err != nil {
		return toolMetrics{}, fmt.Errorf("create MCP in-flight counter: %w", err)
	}
	return result, nil
}

func addTool[In, Out any](server *mcp.Server, instrumentation toolInstrumentation, tool *mcp.Tool, handler func(context.Context, *mcp.CallToolRequest, In) (Out, error)) {
	toolName := tool.Name
	mcp.AddTool(server, tool, func(ctx context.Context, request *mcp.CallToolRequest, input In) (*mcp.CallToolResult, Out, error) {
		requestID, err := newRequestID()
		if err != nil {
			var zero Out
			return nil, zero, fmt.Errorf("assign tool request id: %w", err)
		}
		ctx = context.WithValue(ctx, requestIDKey{}, requestID)
		ctx, span := instrumentation.tracer.Start(ctx, "mcp.tool."+toolName, trace.WithAttributes(
			attribute.String("mcp.tool.name", toolName),
			attribute.String("mcp.transport", instrumentation.transport),
			attribute.String("request.id", requestID),
		))
		started := time.Now()
		attrs := metric.WithAttributes(attribute.String("mcp.tool.name", toolName), attribute.String("mcp.transport", instrumentation.transport))
		instrumentation.metrics.inflight.Add(ctx, 1, attrs)
		var out Out
		var callErr error
		func() {
			defer instrumentation.metrics.inflight.Add(ctx, -1, attrs)
			select {
			case instrumentation.reads <- struct{}{}:
				defer func() { <-instrumentation.reads }()
				out, callErr = handler(ctx, request, input)
			default:
				callErr = errReadCapacity
			}
		}()
		outcome := "success"
		level := slog.LevelInfo
		if callErr != nil {
			outcome = "error"
			level = slog.LevelError
			span.RecordError(errors.New("tool handler failed"))
			span.SetStatus(codes.Error, "tool handler failed")
		}
		fields := []any{
			"service", "ara-mcp", "version", instrumentation.version,
			"component", "tools", "event", "tool_call", "transport", instrumentation.transport,
			"request_id", requestID, "tool", toolName, "outcome", outcome,
			"duration_seconds", time.Since(started).Seconds(),
		}
		spanContext := span.SpanContext()
		if spanContext.IsValid() {
			fields = append(fields, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
		}
		if callErr != nil {
			fields = append(fields, "error_class", toolErrorClass(callErr))
		}
		instrumentation.logger.Log(ctx, level, "tool call completed", fields...)
		span.End()
		attrs = metric.WithAttributes(attribute.String("mcp.tool.name", toolName), attribute.String("mcp.transport", instrumentation.transport), attribute.String("mcp.outcome", outcome))
		instrumentation.metrics.calls.Add(ctx, 1, attrs)
		instrumentation.metrics.duration.Record(ctx, time.Since(started).Seconds(), attrs)
		return nil, out, callErr
	})
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func toolErrorClass(err error) string {
	if errors.Is(err, errReadCapacity) {
		return "capacity"
	}
	if errors.Is(err, errInvalidArgument) {
		return "invalid_argument"
	}
	var apiError *ara.APIError
	if errors.As(err, &apiError) {
		switch apiError.Status {
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return "invalid_upstream_request"
		case http.StatusNotFound:
			return "not_found"
		case http.StatusConflict, http.StatusLocked:
			return "upstream_conflict"
		case http.StatusServiceUnavailable, http.StatusBadGateway, http.StatusGatewayTimeout:
			return "upstream_unavailable"
		default:
			return "upstream_error"
		}
	}
	var requestError *ara.RequestError
	if errors.As(err, &requestError) {
		switch requestError.Class {
		case "invalid_request":
			return "invalid_argument"
		case "timeout", "cancelled":
			return requestError.Class
		case "network":
			return "upstream_unavailable"
		case "decode", "response_too_large":
			return "invalid_upstream_response"
		default:
			return "upstream_error"
		}
	}
	return "adapter_error"
}

func requestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

// ServerContext contains one point-in-time set of server identity and state reads.
type ServerContext struct {
	Server   ServerInfo     `json:"server"`
	Versions ServerVersions `json:"versions"`
	State    map[string]any `json:"state"`
}

// ServerInfo is Ara's /server/info response.
type ServerInfo struct {
	UUID     string `json:"server_uuid"`
	Nickname string `json:"nickname"`
	Version  string `json:"version"`
	API      string `json:"api"`
	Tier     string `json:"tier"`
}

// ServerVersions describes Ara's REST and WebSocket surfaces.
type ServerVersions struct {
	DaemonVersion string       `json:"daemon_version"`
	DaemonGitSHA  string       `json:"daemon_git_sha"`
	DotnetVersion string       `json:"dotnet_version"`
	APISurfaces   []APISurface `json:"api_surfaces"`
}

// APISurface is one versioned Ara API surface.
type APISurface struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// RigContext is a point-in-time view of Ara's configured profile and devices.
type RigContext struct {
	CurrentProfileID  *string                   `json:"current_profile_id"`
	Site              map[string]any            `json:"site"`
	ImagingDefaults   map[string]any            `json:"imaging_defaults"`
	FilterWheelLabels any                       `json:"filter_wheel_labels"`
	FilterSet         any                       `json:"filter_set"`
	Devices           map[string]DeviceSnapshot `json:"devices"`
}

// DeviceSnapshot explicitly represents a disconnected or unselected device.
type DeviceSnapshot struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Status    map[string]any `json:"status,omitempty"`
}

// SequenceListInput limits the Ara sequence list request.
type SequenceListInput struct {
	Limit int `json:"limit,omitempty" jsonschema:"maximum 100; defaults to 50"`
}

// SequenceInput identifies one saved sequence.
type SequenceInput struct {
	SequenceID string `json:"sequence_id" jsonschema:"Ara sequence UUID"`
}

// AdapterDiagnostics reports local process state and a read-only Ara reachability check.
type AdapterDiagnostics struct {
	Version                string            `json:"version"`
	UptimeSeconds          float64           `json:"uptime_seconds"`
	Transport              string            `json:"transport"`
	AraReachable           bool              `json:"ara_reachable"`
	LastSuccessfulAraCheck *time.Time        `json:"last_successful_ara_check"`
	WebSocketState         string            `json:"websocket_state"`
	LastHeartbeat          *time.Time        `json:"last_heartbeat"`
	LastReconciliation     *time.Time        `json:"last_reconciliation"`
	ControlOwnership       string            `json:"control_ownership"`
	TelemetryOutputs       []string          `json:"telemetry_outputs"`
	Runtime                map[string]string `json:"runtime"`
	Process                monitor.Snapshot  `json:"process"`
}

type araHealth struct {
	mu        sync.Mutex
	lastCheck time.Time
	reachable bool
	lastGood  time.Time
	checking  bool
	wait      chan struct{}
}

func (h *araHealth) check(ctx context.Context, client *ara.Client, requestID string) (bool, *time.Time, error) {
	for {
		h.mu.Lock()
		now := time.Now()
		if !h.lastCheck.IsZero() && now.Sub(h.lastCheck) < 5*time.Second {
			reachable, lastGood := h.snapshotLocked()
			h.mu.Unlock()
			return reachable, lastGood, nil
		}
		if h.checking {
			wait := h.wait
			h.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return false, nil, ctx.Err()
			}
		}
		h.checking = true
		h.wait = make(chan struct{})
		wait := h.wait
		h.mu.Unlock()

		_, err := client.Do(ctx, ara.Request{Method: http.MethodGet, Route: "/server/info", RequestID: requestID}, nil)
		h.mu.Lock()
		if ctx.Err() != nil {
			h.checking = false
			close(wait)
			h.mu.Unlock()
			return false, nil, ctx.Err()
		}
		h.lastCheck = time.Now()
		h.reachable = err == nil
		if err == nil {
			h.lastGood = h.lastCheck.UTC()
		}
		h.checking = false
		close(wait)
		reachable, lastGood := h.snapshotLocked()
		h.mu.Unlock()
		return reachable, lastGood, nil
	}
}

func (h *araHealth) snapshotLocked() (bool, *time.Time) {
	if h.lastGood.IsZero() {
		return h.reachable, nil
	}
	last := h.lastGood
	return h.reachable, &last
}

func getServerContext(ctx context.Context, client *ara.Client) (ServerContext, error) {
	var result ServerContext
	requests := []struct {
		route string
		out   any
	}{
		{route: "/server/info", out: &result.Server},
		{route: "/server/versions", out: &result.Versions},
		{route: "/server/state", out: &result.State},
	}
	for _, request := range requests {
		if _, err := client.Do(ctx, ara.Request{Method: http.MethodGet, Route: request.route, RequestID: requestID(ctx)}, request.out); err != nil {
			return ServerContext{}, fmt.Errorf("read Ara %s: %w", request.route, err)
		}
	}
	return result, nil
}

func getRigContext(ctx context.Context, client *ara.Client) (RigContext, error) {
	var state struct {
		CurrentProfileID *string `json:"current_profile_id"`
	}
	if _, err := client.Do(ctx, ara.Request{Method: http.MethodGet, Route: "/server/state", RequestID: requestID(ctx)}, &state); err != nil {
		return RigContext{}, fmt.Errorf("read Ara /server/state: %w", err)
	}
	result := RigContext{
		CurrentProfileID: state.CurrentProfileID,
		Devices: map[string]DeviceSnapshot{
			"camera": {Reason: "unavailable"}, "telescope": {Reason: "unavailable"},
			"focuser": {Reason: "unavailable"}, "filterwheel": {Reason: "unavailable"},
		},
	}
	for _, request := range []struct {
		route string
		out   any
	}{
		{route: "/profile/site", out: &result.Site},
		{route: "/profile/imaging-defaults", out: &result.ImagingDefaults},
		{route: "/profile/filter-wheel/labels", out: &result.FilterWheelLabels},
		{route: "/profile/filter-set", out: &result.FilterSet},
	} {
		if _, err := client.Do(ctx, ara.Request{Method: http.MethodGet, Route: request.route, RequestID: requestID(ctx)}, request.out); err != nil {
			return RigContext{}, fmt.Errorf("read Ara %s: %w", request.route, err)
		}
	}
	for _, device := range []string{"camera", "telescope", "focuser", "filterwheel"} {
		var status map[string]any
		_, err := client.Do(ctx, ara.Request{Method: http.MethodGet, Route: "/equipment/" + device, RequestID: requestID(ctx)}, &status)
		if err != nil {
			var apiError *ara.APIError
			if errors.As(err, &apiError) && apiError.Status == http.StatusNotFound {
				continue
			}
			return RigContext{}, fmt.Errorf("read Ara equipment %s: %w", device, err)
		}
		result.Devices[device] = DeviceSnapshot{Available: true, Status: status}
	}
	return result, nil
}

func listSequences(ctx context.Context, client *ara.Client, limit int) (ara.Page[jsontext.Value], error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return ara.Page[jsontext.Value]{}, fmt.Errorf("%w: sequence limit must be between 1 and 100", errInvalidArgument)
	}
	page, _, err := client.ListSequencesWithRequestID(ctx, limit, requestID(ctx))
	if err != nil {
		return ara.Page[jsontext.Value]{}, fmt.Errorf("list Ara sequences: %w", err)
	}
	return page, nil
}

func getSequence(ctx context.Context, client *ara.Client, id string) (jsontext.Value, error) {
	var result jsontext.Value
	if _, err := client.Do(ctx, ara.Request{
		Method:     http.MethodGet,
		Route:      "/sequences/{id}",
		PathParams: map[string]string{"id": id},
		RequestID:  requestID(ctx),
	}, &result); err != nil {
		return nil, fmt.Errorf("read Ara sequence: %w", err)
	}
	return result, nil
}

func getAdapterDiagnostics(ctx context.Context, options Options, health *araHealth) (AdapterDiagnostics, error) {
	result := AdapterDiagnostics{
		Version:          options.Version,
		UptimeSeconds:    time.Since(options.StartedAt).Seconds(),
		Transport:        options.Transport,
		WebSocketState:   "not_connected",
		ControlOwnership: "not_owned",
		TelemetryOutputs: []string{"in_process"},
		Runtime:          monitor.RuntimeInfo(),
		Process:          options.Sampler.Snapshot(),
	}
	reachable, lastSuccessful, err := health.check(ctx, options.Ara, requestID(ctx))
	if err != nil {
		return AdapterDiagnostics{}, fmt.Errorf("check Ara reachability: %w", err)
	}
	result.AraReachable = reachable
	result.LastSuccessfulAraCheck = lastSuccessful
	return result, nil
}
