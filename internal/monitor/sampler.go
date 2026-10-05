// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

// Package monitor samples bounded process and Go runtime statistics.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const sampleInterval = 2 * time.Second

// Snapshot reports adapter-local process state, not Ara or equipment health.
type Snapshot struct {
	SampledAt     time.Time `json:"sampled_at"`
	UptimeSeconds float64   `json:"uptime_seconds"`
	CPUPercent    float64   `json:"cpu_percent"`
	CPUAvailable  bool      `json:"cpu_available"`
	LogicalCPUs   int       `json:"logical_cpus"`
	RSSBytes      uint64    `json:"rss_bytes"`
	RSSAvailable  bool      `json:"rss_available"`
	GoAllocBytes  uint64    `json:"go_alloc_bytes"`
	GoSysBytes    uint64    `json:"go_sys_bytes"`
	Goroutines    int       `json:"goroutines"`
	GOMaxProcs    int       `json:"gomaxprocs"`
	HeapObjects   uint64    `json:"heap_objects"`
	GCCount       uint32    `json:"gc_count"`
}

type processStats struct {
	cpu time.Duration
	rss uint64
}

type samplerMetrics struct {
	cpu         metric.Float64Gauge
	rss         metric.Int64Gauge
	goAlloc     metric.Int64Gauge
	goSys       metric.Int64Gauge
	heapObjects metric.Int64Gauge
	gcCount     metric.Int64Gauge
	goroutines  metric.Int64Gauge
	logicalCPUs metric.Int64Gauge
	goMaxProcs  metric.Int64Gauge
	uptime      metric.Float64Gauge
}

// Sampler returns one shared, paced process/runtime snapshot.
type Sampler struct {
	mu       sync.Mutex
	started  time.Time
	lastAt   time.Time
	lastProc processStats
	procOK   bool
	last     Snapshot
	metrics  samplerMetrics
	now      func() time.Time
	readProc func() (processStats, error)
}

// NewSampler creates a sampler backed by the process platform's native counters.
func NewSampler() *Sampler {
	now := time.Now
	return &Sampler{started: now(), now: now, readProc: readProcessStats}
}

// NewSamplerWithMeter creates a sampler and its bounded process gauges.
func NewSamplerWithMeter(meter metric.Meter) (*Sampler, error) {
	if meter == nil {
		return nil, errors.New("metrics meter is required")
	}
	result := NewSampler()
	var err error
	if result.metrics.cpu, err = meter.Float64Gauge("process.cpu.percent", metric.WithUnit("%"), metric.WithDescription("Process CPU percentage; one logical CPU is 100 percent")); err != nil {
		return nil, fmt.Errorf("create process CPU gauge: %w", err)
	}
	if result.metrics.rss, err = meter.Int64Gauge("process.memory.rss", metric.WithUnit("By"), metric.WithDescription("Process resident memory")); err != nil {
		return nil, fmt.Errorf("create process RSS gauge: %w", err)
	}
	if result.metrics.goSys, err = meter.Int64Gauge("process.memory.go_sys", metric.WithUnit("By"), metric.WithDescription("Go runtime system memory")); err != nil {
		return nil, fmt.Errorf("create Go memory gauge: %w", err)
	}
	if result.metrics.goAlloc, err = meter.Int64Gauge("process.memory.go_alloc", metric.WithUnit("By"), metric.WithDescription("Go heap allocation in use")); err != nil {
		return nil, fmt.Errorf("create Go allocated-memory gauge: %w", err)
	}
	if result.metrics.heapObjects, err = meter.Int64Gauge("process.memory.heap_objects", metric.WithUnit("{object}"), metric.WithDescription("Go heap object count")); err != nil {
		return nil, fmt.Errorf("create Go heap object gauge: %w", err)
	}
	if result.metrics.gcCount, err = meter.Int64Gauge("process.gc.count", metric.WithUnit("{collection}"), metric.WithDescription("Go garbage collection count")); err != nil {
		return nil, fmt.Errorf("create Go GC gauge: %w", err)
	}
	if result.metrics.goroutines, err = meter.Int64Gauge("process.goroutines", metric.WithUnit("{goroutine}"), metric.WithDescription("Current goroutine count")); err != nil {
		return nil, fmt.Errorf("create goroutine gauge: %w", err)
	}
	if result.metrics.logicalCPUs, err = meter.Int64Gauge("process.logical_cpus", metric.WithUnit("{cpu}"), metric.WithDescription("Logical CPU count available to the Go runtime")); err != nil {
		return nil, fmt.Errorf("create logical CPU gauge: %w", err)
	}
	if result.metrics.goMaxProcs, err = meter.Int64Gauge("process.gomaxprocs", metric.WithUnit("{thread}"), metric.WithDescription("Current Go runtime GOMAXPROCS setting")); err != nil {
		return nil, fmt.Errorf("create GOMAXPROCS gauge: %w", err)
	}
	if result.metrics.uptime, err = meter.Float64Gauge("process.uptime", metric.WithUnit("s"), metric.WithDescription("Adapter process uptime")); err != nil {
		return nil, fmt.Errorf("create process uptime gauge: %w", err)
	}
	return result, nil
}

// Snapshot returns a cached sample until the two-second cadence has elapsed.
func (s *Sampler) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if !s.lastAt.IsZero() && now.Sub(s.lastAt) < sampleInterval {
		return s.last
	}
	current, processErr := s.readProc()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	sample := Snapshot{
		SampledAt:     now.UTC(),
		UptimeSeconds: now.Sub(s.started).Seconds(),
		LogicalCPUs:   runtime.NumCPU(),
		GoAllocBytes:  mem.Alloc,
		GoSysBytes:    mem.Sys,
		Goroutines:    runtime.NumGoroutine(),
		GOMaxProcs:    runtime.GOMAXPROCS(0),
		HeapObjects:   mem.HeapObjects,
		GCCount:       mem.NumGC,
	}
	if processErr == nil {
		sample.RSSBytes, sample.RSSAvailable = current.rss, true
		if s.procOK && now.After(s.lastAt) {
			delta := current.cpu - s.lastProc.cpu
			if delta >= 0 {
				sample.CPUPercent = float64(delta) / float64(now.Sub(s.lastAt)) * 100
				sample.CPUAvailable = true
			}
		}
		s.lastProc, s.procOK = current, true
	} else {
		s.procOK = false
	}
	s.lastAt, s.last = now, sample
	attrs := metric.WithAttributes(attribute.String("service", "ara-mcp"))
	if s.metrics.goAlloc != nil {
		s.metrics.goAlloc.Record(context.Background(), int64(sample.GoAllocBytes), attrs)
	}
	if s.metrics.goSys != nil {
		s.metrics.goSys.Record(context.Background(), int64(sample.GoSysBytes), attrs)
	}
	if s.metrics.heapObjects != nil {
		s.metrics.heapObjects.Record(context.Background(), int64(sample.HeapObjects), attrs)
	}
	if s.metrics.gcCount != nil {
		s.metrics.gcCount.Record(context.Background(), int64(sample.GCCount), attrs)
	}
	if s.metrics.goroutines != nil {
		s.metrics.goroutines.Record(context.Background(), int64(sample.Goroutines), attrs)
	}
	if s.metrics.logicalCPUs != nil {
		s.metrics.logicalCPUs.Record(context.Background(), int64(sample.LogicalCPUs), attrs)
	}
	if s.metrics.goMaxProcs != nil {
		s.metrics.goMaxProcs.Record(context.Background(), int64(sample.GOMaxProcs), attrs)
	}
	if s.metrics.uptime != nil {
		s.metrics.uptime.Record(context.Background(), sample.UptimeSeconds, attrs)
	}
	if sample.RSSAvailable && s.metrics.rss != nil {
		s.metrics.rss.Record(context.Background(), int64(sample.RSSBytes), attrs)
	}
	if sample.CPUAvailable && s.metrics.cpu != nil {
		s.metrics.cpu.Record(context.Background(), sample.CPUPercent, attrs)
	}
	return sample
}

// RuntimeInfo returns the Go build and runtime versions without exposing settings.
func RuntimeInfo() map[string]string {
	info, ok := debug.ReadBuildInfo()
	version := "unknown"
	if ok {
		version = info.GoVersion
	}
	return map[string]string{"go_version": version, "goos": runtime.GOOS, "goarch": runtime.GOARCH}
}
