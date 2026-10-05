// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/mcpserver"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Serve starts the configured MCP transport. Ara is not contacted until a tool call.
func Serve(ctx context.Context, config Config, version string, stderr io.Writer) error {
	if config.Transport != "stdio" {
		return fmt.Errorf("HTTP MCP transport is not implemented yet")
	}
	if stderr == nil {
		stderr = os.Stderr
	}
	logger := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: parseLogLevel(config.LogLevel)}))
	client, err := ara.New(ara.Config{BaseURL: config.AraURL, Timeout: config.Timeout, ReadRetries: config.ReadRetries})
	if err != nil {
		return fmt.Errorf("create Ara client: %w", err)
	}
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	tracerProvider := sdktrace.NewTracerProvider()
	sampler, err := monitor.NewSamplerWithMeter(meterProvider.Meter("github.com/cavenine/ara-mcp/internal/monitor"))
	if err != nil {
		return fmt.Errorf("create process sampler: %w", err)
	}
	server, err := mcpserver.New(mcpserver.Options{
		Ara: client, Version: version, Transport: "stdio", Logger: logger,
		Meter:   meterProvider.Meter("github.com/cavenine/ara-mcp/internal/mcpserver"),
		Tracer:  tracerProvider.Tracer("github.com/cavenine/ara-mcp/internal/mcpserver"),
		Sampler: sampler, StartedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	logger.InfoContext(ctx, "adapter started", "service", "ara-mcp", "version", version, "component", "process", "event", "startup", "transport", "stdio")
	runErr := server.Run(ctx, &mcp.StdioTransport{})
	if errors.Is(runErr, context.Canceled) && ctx.Err() != nil {
		runErr = nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := errors.Join(runErr, meterProvider.Shutdown(shutdownCtx), tracerProvider.Shutdown(shutdownCtx)); err != nil {
		return fmt.Errorf("run MCP server: %w", err)
	}
	logger.InfoContext(ctx, "adapter stopped", "service", "ara-mcp", "version", version, "component", "process", "event", "shutdown", "transport", "stdio")
	return nil
}

func parseLogLevel(value string) slog.Level {
	switch value {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
