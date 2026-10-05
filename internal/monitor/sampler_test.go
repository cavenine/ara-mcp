// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"errors"
	"slices"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestSamplerSharesPacedProcessSnapshot(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	sampler, err := NewSamplerWithMeter(provider.Meter("test"))
	if err != nil {
		t.Fatal(err)
	}
	first := sampler.Snapshot()
	second := sampler.Snapshot()
	if first.SampledAt.IsZero() || first.SampledAt != second.SampledAt {
		t.Fatalf("sampling was not paced: first=%+v second=%+v", first, second)
	}
	if first.LogicalCPUs < 1 || first.Goroutines < 1 || first.GoSysBytes == 0 {
		t.Fatalf("missing runtime/process sample: %+v", first)
	}
	sampler.mu.Lock()
	sampler.lastAt = time.Now().Add(-sampleInterval)
	sampler.procOK = true
	sampler.lastProc = processStats{}
	sampler.mu.Unlock()
	sampler.Snapshot()
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, scope := range collected.ScopeMetrics {
		for _, item := range scope.Metrics {
			names = append(names, item.Name)
		}
	}
	for _, name := range []string{
		"process.cpu.percent", "process.memory.rss", "process.memory.go_alloc",
		"process.memory.go_sys", "process.memory.heap_objects", "process.gc.count",
		"process.goroutines", "process.logical_cpus", "process.gomaxprocs", "process.uptime",
	} {
		if !slices.Contains(names, name) {
			t.Errorf("runtime gauge %q missing from metrics: %v", name, names)
		}
	}
}

func TestSamplerCPUWarmupPacingAndRecovery(t *testing.T) {
	now := time.Unix(1_000, 0)
	proc := processStats{cpu: time.Second, rss: 1024}
	readErr := errors.New("process counters unavailable")
	sampler := NewSampler()
	sampler.started = now
	sampler.now = func() time.Time { return now }
	sampler.readProc = func() (processStats, error) {
		if readErr != nil {
			return processStats{}, readErr
		}
		return proc, nil
	}
	first := sampler.Snapshot()
	if first.CPUAvailable || first.RSSAvailable {
		t.Fatalf("failed initial counters reported available: %+v", first)
	}
	readErr = nil
	now = now.Add(sampleInterval)
	second := sampler.Snapshot()
	if second.CPUAvailable {
		t.Fatalf("first valid counter sample reported CPU before a baseline: %+v", second)
	}
	proc.cpu += 200 * time.Millisecond
	now = now.Add(sampleInterval)
	third := sampler.Snapshot()
	if !third.CPUAvailable || third.CPUPercent < 9.99 || third.CPUPercent > 10.01 || !third.RSSAvailable || third.RSSBytes != 1024 {
		t.Fatalf("calculated process sample = %+v", third)
	}
	now = now.Add(time.Second)
	if paced := sampler.Snapshot(); paced.SampledAt != third.SampledAt {
		t.Fatalf("sampled before interval: got %v, want %v", paced.SampledAt, third.SampledAt)
	}
}
