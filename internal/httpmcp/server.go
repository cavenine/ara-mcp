// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpmcp mounts the official Streamable HTTP transport behind the
// adapter's shared HTTP access and logging boundary.
package httpmcp

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cavenine/ara-mcp/internal/diagnostics"
	"github.com/go-chi/chi/v5"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Handler mounts the SDK transport at /mcp with shared-operator bearer auth and
// same-origin protection. The SDK retains ownership of all MCP response framing.
func Handler(server *mcp.Server, bearerToken string, origins []string, logger *slog.Logger, meter metric.Meter, tracer trace.Tracer) (http.Handler, error) {
	if server == nil || len(bearerToken) < 32 {
		return nil, errors.New("MCP server and bearer token of at least 32 characters are required")
	}
	sdkHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		MaxRequestBodyBytes:          2 << 20,
		PropagateRequestCancellation: true,
	})
	return composeHandler(sdkHandler, bearerToken, origins, logger, meter, tracer)
}

func composeHandler(endpoint http.Handler, bearerToken string, origins []string, logger *slog.Logger, meter metric.Meter, tracer trace.Tracer) (http.Handler, error) {
	if endpoint == nil || len(bearerToken) < 32 {
		return nil, errors.New("MCP handler and bearer token of at least 32 characters are required")
	}
	if meter == nil {
		meter = metricnoop.NewMeterProvider().Meter("github.com/cavenine/ara-mcp/internal/httpmcp")
	}
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer("github.com/cavenine/ara-mcp/internal/httpmcp")
	}
	requests, err := meter.Int64Counter("mcp.http.requests", metric.WithUnit("{request}"), metric.WithDescription("Streamable HTTP requests by normalized route and outcome"))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request counter: %w", err)
	}
	duration, err := meter.Float64Histogram("mcp.http.duration", metric.WithUnit("s"), metric.WithDescription("Streamable HTTP request lifetime"))
	if err != nil {
		return nil, fmt.Errorf("create HTTP request duration: %w", err)
	}
	inflight, err := meter.Int64UpDownCounter("mcp.http.in_flight", metric.WithUnit("{request}"), metric.WithDescription("Streamable HTTP requests currently open, including SSE"))
	if err != nil {
		return nil, fmt.Errorf("create HTTP in-flight counter: %w", err)
	}
	faults, err := meter.Int64Counter("mcp.http.faults", metric.WithUnit("{fault}"), metric.WithDescription("Recovered MCP HTTP handler faults, including faults after response headers are committed"))
	if err != nil {
		return nil, fmt.Errorf("create HTTP fault counter: %w", err)
	}
	protection := http.NewCrossOriginProtection()
	for _, origin := range origins {
		if err := protection.AddTrustedOrigin(origin); err != nil {
			return nil, fmt.Errorf("add trusted MCP origin: %w", err)
		}
	}
	verifier := func(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		if len(token) != len(bearerToken) || subtle.ConstantTimeCompare([]byte(token), []byte(bearerToken)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{UserID: "operator"}, nil
	}
	secured := protection.Handler(auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(endpoint))
	router := chi.NewRouter()
	requestMiddleware := diagnostics.MiddlewareForFaults(logger, "mcp_http", func(r *http.Request) {
		if state, ok := r.Context().Value(requestStateKey{}).(*requestState); ok {
			state.fault = true
		}
	})
	router.Use(func(next http.Handler) http.Handler {
		next = requestMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state := new(requestState)
			r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, state))
			ctx, span := tracer.Start(r.Context(), "mcp.http.request", trace.WithAttributes(attribute.String("http.request.method", normalizeMethod(r.Method))))
			r = r.WithContext(ctx)
			started := time.Now()
			inflight.Add(ctx, 1, metric.WithAttributes(attribute.String("mcp.transport", "http")))
			response := &responseWriter{ResponseWriter: w}
			defer func() {
				inflight.Add(ctx, -1, metric.WithAttributes(attribute.String("mcp.transport", "http")))
				elapsed := time.Since(started).Seconds()
				route := chi.RouteContext(r.Context()).RoutePattern()
				if route == "" {
					route = "unmatched"
				}
				status := response.status
				if status == 0 {
					status = http.StatusOK
				}
				outcome := "success"
				if state.fault || status >= http.StatusInternalServerError {
					outcome = "error"
					span.SetStatus(codes.Error, "HTTP request fault")
				}
				attrs := metric.WithAttributes(attribute.String("mcp.transport", "http"), attribute.String("http.route", route), attribute.String("http.request.method", normalizeMethod(r.Method)), attribute.String("http.response.status_class", fmt.Sprintf("%dxx", status/100)), attribute.String("mcp.outcome", outcome))
				requests.Add(ctx, 1, attrs)
				if state.fault {
					faults.Add(ctx, 1, attrs)
				}
				duration.Record(ctx, elapsed, attrs)
				span.SetAttributes(attribute.String("http.route", route), attribute.Int("http.response.status_code", status))
				span.End()
			}()
			next.ServeHTTP(response, r)
		})
	})
	router.Handle("/mcp", secured)
	return router, nil
}

type requestStateKey struct{}

type requestState struct{ fault bool }

func normalizeMethod(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodHead, http.MethodOptions:
		return strings.ToUpper(method)
	default:
		return "OTHER"
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *responseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
