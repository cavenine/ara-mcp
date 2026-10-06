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
	sampler, err := NewSamplerWithMeter(provider.Meter("test"), DefaultSamplerConfig())
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
	sampler.lastAt = time.Now().Add(-defaultSampleInterval)
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
	now = now.Add(defaultSampleInterval)
	second := sampler.Snapshot()
	if second.CPUAvailable {
		t.Fatalf("first valid counter sample reported CPU before a baseline: %+v", second)
	}
	proc.cpu += 200 * time.Millisecond
	now = now.Add(defaultSampleInterval)
	third := sampler.Snapshot()
	if !third.CPUAvailable || third.CPUPercent < 9.99 || third.CPUPercent > 10.01 || !third.RSSAvailable || third.RSSBytes != 1024 {
		t.Fatalf("calculated process sample = %+v", third)
	}
	now = now.Add(time.Second)
	if paced := sampler.Snapshot(); paced.SampledAt != third.SampledAt {
		t.Fatalf("sampled before interval: got %v, want %v", paced.SampledAt, third.SampledAt)
	}
}

func TestSamplerRetainsBoundedOrderedHistory(t *testing.T) {
	now := time.Unix(1_000, 0)
	sampler := NewSampler()
	sampler.now = func() time.Time { return now }
	sampler.readProc = func() (processStats, error) { return processStats{}, errors.New("unavailable") }
	for range 3 {
		sampler.Snapshot()
		now = now.Add(defaultSampleInterval)
	}
	history := sampler.History()
	if len(history) != 3 {
		t.Fatalf("history length = %d, want 3", len(history))
	}
	if history[0].SampleSequence == 0 || history[2].SampleSequence != 3 {
		t.Fatalf("history has no ordered sample identities: %+v", history)
	}
	history[0].Goroutines = -1
	if sampler.History()[0].Goroutines == -1 {
		t.Fatal("history exposed mutable internal storage")
	}
}

func TestSamplerConfigBoundsHistoryAndSubscribers(t *testing.T) {
	config := SamplerConfig{Interval: time.Second, HistorySamples: 2, HistoryAge: time.Hour, Subscribers: 1}
	sampler, err := NewSamplerWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0)
	sampler.started = now
	sampler.now = func() time.Time { return now }
	sampler.readProc = func() (processStats, error) { return processStats{}, errors.New("unavailable") }
	for range 3 {
		sampler.Snapshot()
		now = now.Add(config.Interval)
	}
	history := sampler.History()
	if len(history) != 2 || history[0].SampleSequence != 2 || history[1].SampleSequence != 3 {
		t.Fatalf("configured history = %+v", history)
	}
	_, unsubscribe, ok := sampler.Subscribe()
	if !ok {
		t.Fatal("configured subscriber limit rejected its first subscriber")
	}
	defer unsubscribe()
	if _, _, ok := sampler.Subscribe(); ok {
		t.Fatal("configured subscriber limit accepted a second subscriber")
	}
}

func TestSamplerEvictsHistoryByConfiguredAge(t *testing.T) {
	config := SamplerConfig{Interval: time.Second, HistorySamples: 10, HistoryAge: 1500 * time.Millisecond, Subscribers: 1}
	sampler, err := NewSamplerWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_000, 0)
	sampler.started = now
	sampler.now = func() time.Time { return now }
	sampler.readProc = func() (processStats, error) { return processStats{}, errors.New("unavailable") }
	for range 4 {
		sampler.Snapshot()
		now = now.Add(config.Interval)
	}
	history := sampler.History()
	if len(history) != 2 || history[0].SampleSequence != 3 || history[1].SampleSequence != 4 {
		t.Fatalf("age-bounded history = %+v, want sequences 3 and 4", history)
	}
}

func TestSamplerSubscriptionsCoalesceLatestAndRespectLimit(t *testing.T) {
	now := time.Unix(1_000, 0)
	sampler := NewSampler()
	sampler.now = func() time.Time { return now }
	sampler.readProc = func() (processStats, error) { return processStats{}, errors.New("unavailable") }
	first := sampler.Snapshot()
	updates, unsubscribe, ok := sampler.Subscribe()
	if !ok {
		t.Fatal("first subscriber rejected")
	}
	defer unsubscribe()
	if got := <-updates; got.SampleSequence != first.SampleSequence {
		t.Fatalf("initial sample = %+v, want sequence %d", got, first.SampleSequence)
	}
	now = now.Add(defaultSampleInterval)
	second := sampler.Snapshot()
	now = now.Add(defaultSampleInterval)
	third := sampler.Snapshot()
	if got := <-updates; got.SampleSequence != third.SampleSequence {
		t.Fatalf("coalesced sample = %d, want latest sequence %d", got.SampleSequence, third.SampleSequence)
	}
	for range 3 {
		if _, release, ok := sampler.Subscribe(); !ok {
			t.Fatal("subscriber capacity reached early")
		} else {
			defer release()
		}
	}
	if _, release, ok := sampler.Subscribe(); ok {
		release()
		t.Fatal("subscriber limit was not enforced")
	}
	_ = second
}
