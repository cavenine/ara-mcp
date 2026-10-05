// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json/jsontext"
	"io"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestTerminalRunObservationsAreCountedOnceWithoutIDLabels(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	metrics, err := newMetrics(provider.Meter("sequence-observation-test"))
	if err != nil {
		t.Fatal(err)
	}
	tracker := &sequenceObservationTracker{
		seen: make(map[string]struct{}), terminal: metrics.terminalRuns,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), version: "test", transport: "stdio",
	}
	const state = `{"sequence_id":"7db1118d-57d9-4657-9779-8e1e619b12c4","run_id":"c216e1ba-1929-4c2a-9b14-a8da3777dd42","state":"completed"}`
	tracker.observe(t.Context(), jsontext.Value(state))
	tracker.observe(t.Context(), jsontext.Value(state))
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	var count int64
	for _, scope := range collected.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != "ara.sequence.runs.observed" {
				continue
			}
			sum, ok := recorded.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("terminal observation data = %T, want integer sum", recorded.Data)
			}
			for _, point := range sum.DataPoints {
				count += point.Value
				if point.Attributes.Len() != 1 {
					t.Errorf("terminal observation labels = %v, want bounded state only", point.Attributes)
				}
				if state, ok := point.Attributes.Value(attribute.Key("ara.sequence.state")); !ok || state.AsString() != "completed" {
					t.Errorf("terminal state label = %v, want completed", point.Attributes)
				}
			}
		}
	}
	if count != 1 {
		t.Fatalf("terminal observations = %d, want one for the same sequence/run ID", count)
	}
}
