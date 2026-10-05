// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"log/slog"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const maxObservedTerminalRuns = 1024

type sequenceObservationTracker struct {
	mu        sync.Mutex
	seen      map[string]struct{}
	terminal  metric.Int64Counter
	logger    *slog.Logger
	version   string
	transport string
}

func (t *sequenceObservationTracker) observe(ctx context.Context, body jsontext.Value) {
	var run sequenceRun
	if json.Unmarshal(body, &run) != nil || run.SequenceID == "" || run.RunID == "" {
		return
	}
	state := strings.ToLower(run.State)
	if !terminalRunState(state) {
		return
	}
	key := run.SequenceID + "/" + run.RunID
	t.mu.Lock()
	if _, exists := t.seen[key]; exists {
		t.mu.Unlock()
		return
	}
	if len(t.seen) >= maxObservedTerminalRuns {
		// ponytail: stop counting distinct terminal runs at 1,024 entries rather
		// than forget old IDs and double-count replays; raise the cap if needed.
		t.mu.Unlock()
		return
	}
	t.seen[key] = struct{}{}
	t.mu.Unlock()
	t.terminal.Add(ctx, 1, metric.WithAttributes(attribute.String("ara.sequence.state", state)))
	fields := []any{"service", "ara-mcp", "version", t.version, "component", "sequence", "event", "sequence_run_terminal_observed",
		"transport", t.transport, "request_id", requestID(ctx), "sequence_id", run.SequenceID, "run_id", run.RunID, "state", state}
	if span := trace.SpanFromContext(ctx).SpanContext(); span.IsValid() {
		fields = append(fields, "trace_id", span.TraceID().String(), "span_id", span.SpanID().String())
	}
	t.logger.InfoContext(ctx, "Ara sequence run terminal state observed", fields...)
}

func terminalRunState(state string) bool {
	switch state {
	case "completed", "stopped", "failed":
		return true
	default:
		return false
	}
}
