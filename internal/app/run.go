// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/diagnostics"
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
	var meterProvider *sdkmetric.MeterProvider
	var metricsHandler http.Handler
	if config.DiagnosticsListen != "" {
		meterProvider, metricsHandler, err = diagnostics.NewMetrics()
		if err != nil {
			return err
		}
	} else {
		meterProvider = sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader()))
	}
	tracerProvider := sdktrace.NewTracerProvider()
	sampler, err := monitor.NewSamplerWithMeter(meterProvider.Meter("github.com/cavenine/ara-mcp/internal/monitor"))
	if err != nil {
		return fmt.Errorf("create process sampler: %w", err)
	}
	control, err := mcpserver.NewControlManager(
		client, logger, version, config.Transport,
		meterProvider.Meter("github.com/cavenine/ara-mcp/internal/mcpserver/control"),
		tracerProvider.Tracer("github.com/cavenine/ara-mcp/internal/mcpserver/control"),
	)
	if err != nil {
		return fmt.Errorf("create control manager: %w", err)
	}
	server, err := mcpserver.New(mcpserver.Options{
		Ara: client, Control: control, Version: version, Transport: "stdio", Logger: logger,
		Meter:   meterProvider.Meter("github.com/cavenine/ara-mcp/internal/mcpserver"),
		Tracer:  tracerProvider.Tracer("github.com/cavenine/ara-mcp/internal/mcpserver"),
		Sampler: sampler, StartedAt: time.Now(),
	})
	if err != nil {
		return fmt.Errorf("create MCP server: %w", err)
	}
	var diagnosticsServer *http.Server
	var diagnosticsListener net.Listener
	diagnosticsErrors := make(chan error, 1)
	if config.DiagnosticsListen != "" {
		if config.DiagnosticsTLSCert != "" {
			if _, err := tls.LoadX509KeyPair(config.DiagnosticsTLSCert, config.DiagnosticsTLSKey); err != nil {
				return fmt.Errorf("load diagnostics TLS certificate: %w", err)
			}
		}
		handler, err := diagnostics.Handler(diagnostics.Access{
			Listen: config.DiagnosticsListen, Username: config.DiagnosticsUsername, Password: config.DiagnosticsPassword,
			TLSCert: config.DiagnosticsTLSCert, TLSKey: config.DiagnosticsTLSKey,
		}, diagnostics.Runtime{Ara: client, Sampler: sampler, Logger: logger, Version: version, StartedAt: time.Now(), Metrics: metricsHandler})
		if err != nil {
			return fmt.Errorf("configure diagnostics HTTP: %w", err)
		}
		diagnosticsListener, err = net.Listen("tcp", config.DiagnosticsListen)
		if err != nil {
			return fmt.Errorf("listen for diagnostics HTTP: %w", err)
		}
		diagnosticsServer = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	}
	logger.InfoContext(ctx, "adapter started", "service", "ara-mcp", "version", version, "component", "process", "event", "startup", "transport", "stdio")
	if diagnosticsListener != nil {
		logger.InfoContext(ctx, "diagnostics HTTP listener started", "service", "ara-mcp", "version", version, "component", "diagnostics_http", "event", "startup", "address", diagnosticsListener.Addr().String())
	}
	runCtx, stop := context.WithCancel(ctx)
	if diagnosticsServer != nil {
		go func() {
			var serveErr error
			if config.DiagnosticsTLSCert != "" {
				serveErr = diagnosticsServer.ServeTLS(diagnosticsListener, config.DiagnosticsTLSCert, config.DiagnosticsTLSKey)
			} else {
				serveErr = diagnosticsServer.Serve(diagnosticsListener)
			}
			if !errors.Is(serveErr, http.ErrServerClosed) {
				diagnosticsErrors <- serveErr
				stop()
			}
		}()
	}
	runErr := server.Run(runCtx, &mcp.StdioTransport{})
	stop()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	runErr = errors.Join(runErr, control.Close(shutdownCtx, ""))
	shutdownCancel()
	if diagnosticsListener != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := diagnosticsServer.Shutdown(shutdownCtx); err != nil {
			runErr = errors.Join(runErr, err)
		}
		shutdownCancel()
		select {
		case err := <-diagnosticsErrors:
			runErr = errors.Join(runErr, err)
		default:
		}
	}
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
