// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchivePersistsAndRotatesWithinQuota(t *testing.T) {
	directory := t.TempDir()
	archive, err := newArchiveWithLimits(directory, nil, 512, 2, 64)
	if err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(1); sequence <= 10; sequence++ {
		if !archive.Record(Snapshot{SchemaVersion: "1", InstanceID: "instance-1", SampleSequence: sequence, SampledAt: time.Unix(int64(sequence), 0)}) {
			t.Fatalf("sample %d was not queued", sequence)
		}
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	files, err := filepath.Glob(filepath.Join(directory, "resource-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("retained archive segments = %d, want 2: %v", len(files), files)
	}
	var lines int
	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 512 {
			t.Fatalf("segment %s size = %d, want <= 512", path, info.Size())
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines += strings.Count(string(contents), "\n")
	}
	if lines == 0 || lines >= 10 {
		t.Fatalf("retained JSONL records = %d; expected bounded rotated history", lines)
	}

	archive, err = newArchiveWithLimits(directory, nil, 512, 2, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !archive.Record(Snapshot{SchemaVersion: "1", InstanceID: "instance-2", SampleSequence: 1, SampledAt: time.Unix(20, 0)}) {
		t.Fatal("post-restart sample was not queued")
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err = filepath.Glob(filepath.Join(directory, "resource-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 2 {
		t.Fatalf("restart exceeded segment quota: %v", files)
	}
}

func TestArchiveRecoversPartialTailAfterRestart(t *testing.T) {
	directory := t.TempDir()
	archive, err := newArchiveWithLimits(directory, nil, 1024, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !archive.Record(Snapshot{SchemaVersion: "1", InstanceID: "instance-1", SampleSequence: 1, SampledAt: time.Unix(1, 0)}) {
		t.Fatal("sample was not queued")
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(directory, "resource-*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("archive files = %v, %v", files, err)
	}
	file, err := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"schema_version":"1","partial`); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	archive, err = newArchiveWithLimits(directory, nil, 1024, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !archive.Record(Snapshot{SchemaVersion: "1", InstanceID: "instance-1", SampleSequence: 2, SampledAt: time.Unix(2, 0)}) {
		t.Fatal("post-restart sample was not queued")
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "partial") || strings.Count(string(data), "\n") != 2 {
		t.Fatalf("archive tail was not repaired: %q", data)
	}
}

func TestArchiveQueueDropsWithoutBlockingWhenWriterIsBusy(t *testing.T) {
	archive, err := newArchiveWithLimits(t.TempDir(), nil, 1024, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	archive.fileMu.Lock()
	for sequence := uint64(1); sequence <= 4; sequence++ {
		archive.Record(Snapshot{SchemaVersion: "1", InstanceID: "instance", SampleSequence: sequence})
	}
	archive.fileMu.Unlock()
	if archive.Dropped() == 0 {
		t.Fatal("full writer queue did not count dropped samples")
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSamplerContinuesAfterArchiveWriteFailure(t *testing.T) {
	archive, err := newArchiveWithLimits(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), 1024, 2, 8)
	if err != nil {
		t.Fatal(err)
	}
	archive.fileMu.Lock()
	if err := archive.file.Close(); err != nil {
		archive.fileMu.Unlock()
		t.Fatal(err)
	}
	archive.writer = bufio.NewWriterSize(archive.file, 1)
	archive.fileMu.Unlock()
	sampler := NewSampler()
	sampler.SetArchive(archive)
	first := sampler.Snapshot()
	if first.SampleSequence != 1 {
		t.Fatalf("first sample sequence = %d", first.SampleSequence)
	}
	if err := archive.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !archive.failed.Load() {
		t.Fatal("closed archive writer failure was not recorded")
	}
	sampler.mu.Lock()
	sampler.lastAt = sampler.now().Add(-sampler.config.Interval)
	sampler.mu.Unlock()
	second := sampler.Snapshot()
	if second.SampleSequence != 2 {
		t.Fatalf("sampling stopped after archive failure: sample sequence = %d", second.SampleSequence)
	}
}
