// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package diagnostics serves read-only process and Ara health information.
package diagnostics

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/cavenine/ara-mcp/internal/monitor"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/render"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// Access configures the optional diagnostics listener. Remote access must use TLS and Basic auth.
type Access struct {
	Listen   string
	Username string
	Password string
	TLSCert  string
	TLSKey   string
}

// Validate enforces loopback-by-default access and remote TLS/authentication.
func (a Access) Validate() error {
	if (a.Username == "") != (a.Password == "") {
		return errors.New("diagnostics username and password must be configured together")
	}
	if (a.TLSCert == "") != (a.TLSKey == "") {
		return errors.New("diagnostics TLS certificate and key must be configured together")
	}
	if a.Listen == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(a.Listen)
	if err != nil {
		return errors.New("diagnostics listen address must be host:port")
	}
	if port == "" {
		return errors.New("diagnostics listen port is required")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return errors.New("diagnostics listen port must be between 0 and 65535")
	}
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if !loopback && (a.Username == "" || a.Password == "" || a.TLSCert == "" || a.TLSKey == "") {
		return errors.New("remote diagnostics require basic authentication and TLS certificate/key")
	}
	return nil
}

// Runtime holds dependencies shared with the stdio server.
type Runtime struct {
	Ara       *ara.Client
	Sampler   *monitor.Sampler
	Logger    *slog.Logger
	Version   string
	StartedAt time.Time
	Metrics   http.Handler
}

// NewMetrics creates an isolated Prometheus registry and an OpenTelemetry reader for it.
func NewMetrics() (*sdkmetric.MeterProvider, http.Handler, error) {
	registry := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, nil, fmt.Errorf("create Prometheus exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)), promhttp.HandlerFor(registry, promhttp.HandlerOpts{}), nil
}

// Handler builds the read-only diagnostics router.
func Handler(access Access, runtime Runtime) (http.Handler, error) {
	if runtime.Ara == nil || runtime.Sampler == nil || runtime.Logger == nil {
		return nil, errors.New("Ara client, sampler, and logger are required")
	}
	if err := access.Validate(); err != nil {
		return nil, err
	}
	if access.Listen == "" {
		return nil, errors.New("diagnostics listener address is required")
	}
	health := &healthCache{}
	router := chi.NewRouter()
	router.Use(Middleware(runtime.Logger))
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if access.Username != "" {
				username, password, ok := r.BasicAuth()
				validUser := subtle.ConstantTimeCompare([]byte(username), []byte(access.Username)) == 1
				validPassword := subtle.ConstantTimeCompare([]byte(password), []byte(access.Password)) == 1
				if !ok || !validUser || !validPassword {
					w.Header().Set("WWW-Authenticate", `Basic realm="ara-mcp diagnostics", charset="UTF-8"`)
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		render.JSON(w, r, map[string]string{"status": "ok"})
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		reachable, _ := health.check(r.Context(), runtime.Ara, middleware.GetReqID(r.Context()))
		if !reachable {
			render.Status(r, http.StatusServiceUnavailable)
			render.JSON(w, r, map[string]string{"status": "degraded", "reason": "ara_unavailable"})
			return
		}
		render.JSON(w, r, map[string]string{"status": "ready"})
	})
	router.Get("/status", func(w http.ResponseWriter, r *http.Request) {
		reachable, lastSuccessful := health.check(r.Context(), runtime.Ara, middleware.GetReqID(r.Context()))
		render.JSON(w, r, status{Version: runtime.Version, UptimeSeconds: time.Since(runtime.StartedAt).Seconds(), AraReachable: reachable, LastSuccessfulAraCheck: lastSuccessful, Process: runtime.Sampler.Snapshot()})
	})
	if runtime.Metrics != nil {
		router.Handle("/metrics", runtime.Metrics)
	}
	return router, nil
}

// Middleware adds a trusted request ID, structured request logging, and panic recovery.
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return discardRequestID(middleware.RequestID(middleware.RequestLogger(logFormatter{logger: logger})(middleware.Recoverer(next))))
	}
}

type status struct {
	Version                string           `json:"version"`
	UptimeSeconds          float64          `json:"uptime_seconds"`
	AraReachable           bool             `json:"ara_reachable"`
	LastSuccessfulAraCheck *time.Time       `json:"last_successful_ara_check"`
	Process                monitor.Snapshot `json:"process"`
}

type healthCache struct {
	mu        sync.Mutex
	lastCheck time.Time
	lastGood  time.Time
	reachable bool
	checking  bool
	wait      chan struct{}
}

func (h *healthCache) check(ctx context.Context, client *ara.Client, requestID string) (bool, *time.Time) {
	for {
		h.mu.Lock()
		now := time.Now()
		if !h.lastCheck.IsZero() && now.Sub(h.lastCheck) < 5*time.Second {
			reachable, lastGood := h.snapshotLocked()
			h.mu.Unlock()
			return reachable, lastGood
		}
		if h.checking {
			wait := h.wait
			h.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return false, nil
			}
		}
		h.checking = true
		h.wait = make(chan struct{})
		wait := h.wait
		h.mu.Unlock()

		checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := client.CheckServerWithRequestID(checkCtx, requestID)
		cancel()
		h.mu.Lock()
		if ctx.Err() == nil {
			h.lastCheck = time.Now()
			h.reachable = err == nil
			if err == nil {
				h.lastGood = h.lastCheck.UTC()
			}
		}
		h.checking = false
		close(wait)
		reachable, lastGood := h.snapshotLocked()
		h.mu.Unlock()
		return reachable, lastGood
	}
}

func (h *healthCache) snapshotLocked() (bool, *time.Time) {
	if h.lastGood.IsZero() {
		return h.reachable, nil
	}
	last := h.lastGood
	return h.reachable, &last
}

func discardRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(middleware.RequestIDHeader)
		next.ServeHTTP(w, r)
	})
}

type logFormatter struct{ logger *slog.Logger }
type logEntry struct {
	logger    *slog.Logger
	request   *http.Request
	requestID string
	method    string
	failed    bool
}

func (f logFormatter) NewLogEntry(r *http.Request) middleware.LogEntry {
	return &logEntry{logger: f.logger, request: r, requestID: middleware.GetReqID(r.Context()), method: r.Method}
}
func (e *logEntry) Write(status, bytes int, _ http.Header, elapsed time.Duration, _ any) {
	route := chi.RouteContext(e.request.Context()).RoutePattern()
	if route == "" {
		route = "unmatched"
	}
	if status < http.StatusBadRequest && (route == "/healthz" || route == "/readyz" || route == "/metrics") {
		return
	}
	level := slog.LevelInfo
	event := "http_request"
	message := "http request completed"
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	} else if status >= http.StatusBadRequest {
		level = slog.LevelWarn
	}
	if e.failed {
		level, event, message = slog.LevelError, "http_fault", "http request failed"
	}
	e.logger.Log(e.request.Context(), level, message, "service", "ara-mcp", "component", "diagnostics_http", "event", event, "http_request_id", e.requestID, "method", e.method, "route", route, "http_status", status, "response_bytes", bytes, "duration_seconds", elapsed.Seconds())
}
func (e *logEntry) Panic(_ any, _ []byte) {
	e.failed = true
}
