// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package monitor

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	archiveSegmentBytes = 8 << 20
	archiveSegmentCount = 4
	archiveQueueSize    = 64
	archiveFlushPeriod  = 5 * time.Second
)

type archiveSegment struct {
	sequence uint64
	name     string
}

// ArchiveSegment is a fixed-size read handle for one retained JSONL segment.
// The caller owns File and must close it after exporting the snapshot.
type ArchiveSegment struct {
	Name string
	Size int64
	File *os.File
}

// Archive asynchronously stores a bounded, rotating JSONL resource history.
type Archive struct {
	dir             string
	logger          *slog.Logger
	maxSegmentBytes int64
	maxSegments     int
	queue           chan Snapshot
	done            chan struct{}
	queueMu         sync.Mutex
	closed          bool
	fileMu          sync.Mutex
	segments        []archiveSegment
	nextSequence    uint64
	file            *os.File
	writer          *bufio.Writer
	fileSize        int64
	dropped         atomic.Uint64
	reportedDropped atomic.Uint64
	failed          atomic.Bool
}

// OpenArchive enables the selected four-segment, 8 MiB-per-segment JSONL archive.
func OpenArchive(directory string, logger *slog.Logger) (*Archive, error) {
	return newArchiveWithLimits(directory, logger, archiveSegmentBytes, archiveSegmentCount, archiveQueueSize)
}

func newArchiveWithLimits(directory string, logger *slog.Logger, segmentBytes int64, segmentCount, queueSize int) (*Archive, error) {
	if directory == "" || segmentBytes < 256 || segmentCount < 1 || queueSize < 1 {
		return nil, errors.New("resource archive path and positive limits are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create resource archive directory: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read resource archive directory: %w", err)
	}
	segments := make([]archiveSegment, 0, segmentCount)
	for _, entry := range entries {
		sequence, ok := archiveSequence(entry.Name())
		if !ok {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("resource archive segment %q is a symlink", entry.Name())
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("inspect resource archive segment: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("resource archive segment %q is not a regular file", entry.Name())
		}
		segments = append(segments, archiveSegment{sequence: sequence, name: entry.Name()})
	}
	slices.SortFunc(segments, func(a, b archiveSegment) int { return cmp.Compare(a.sequence, b.sequence) })
	archive := &Archive{
		dir: directory, logger: logger, maxSegmentBytes: segmentBytes, maxSegments: segmentCount,
		queue: make(chan Snapshot, queueSize), done: make(chan struct{}), segments: segments,
	}
	for len(archive.segments) > segmentCount {
		if err := archive.removeOldestLocked(); err != nil {
			return nil, err
		}
	}
	if len(archive.segments) == 0 {
		if err := archive.rotateLocked(); err != nil {
			return nil, err
		}
	} else {
		last := archive.segments[len(archive.segments)-1]
		archive.nextSequence = last.sequence + 1
		path := filepath.Join(directory, last.name)
		file, err := os.OpenFile(path, os.O_APPEND|os.O_RDWR, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open resource archive segment: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("stat resource archive segment: %w", err)
		}
		archive.fileSize, err = recoverArchiveTail(file, info.Size())
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("recover resource archive tail: %w", err)
		}
		archive.file, archive.writer = file, bufio.NewWriterSize(file, 16<<10)
		if archive.fileSize >= segmentBytes {
			if err := archive.rotateLocked(); err != nil {
				archive.closeFileLocked()
				return nil, err
			}
		}
	}
	go archive.run()
	return archive, nil
}

func archiveSequence(name string) (uint64, bool) {
	if !strings.HasPrefix(name, "resource-") || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, "resource-"), ".jsonl")
	sequence, err := strconv.ParseUint(value, 10, 64)
	return sequence, err == nil
}

func recoverArchiveTail(file *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return 0, err
	}
	if last[0] == '\n' {
		return size, nil
	}
	for end := size; end > 0; {
		start := max(int64(0), end-4096)
		chunk := make([]byte, end-start)
		if _, err := file.ReadAt(chunk, start); err != nil {
			return 0, err
		}
		if lastNewline := bytes.LastIndexByte(chunk, '\n'); lastNewline >= 0 {
			newSize := start + int64(lastNewline+1)
			if err := file.Truncate(newSize); err != nil {
				return 0, err
			}
			return newSize, nil
		}
		end = start
	}
	if err := file.Truncate(0); err != nil {
		return 0, err
	}
	return 0, nil
}

// Record queues a sample without waiting for disk I/O. False means the archive
// is closed, failed, or its bounded queue is full; sampling continues regardless.
func (a *Archive) Record(sample Snapshot) bool {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.closed {
		return false
	}
	if a.failed.Load() {
		a.dropped.Add(1)
		return false
	}
	select {
	case a.queue <- sample:
		return true
	default:
		a.dropped.Add(1)
		return false
	}
}

// Dropped returns the number of samples that could not be queued since startup.
func (a *Archive) Dropped() uint64 { return a.dropped.Load() }

// OpenSnapshot opens fixed-length handles for the currently retained segments.
// Rotation or later appends cannot change these handles' exported byte ranges.
func (a *Archive) OpenSnapshot() ([]ArchiveSegment, error) {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	if a.writer != nil {
		if err := a.writer.Flush(); err != nil {
			return nil, fmt.Errorf("flush resource archive snapshot: %w", err)
		}
	}
	result := make([]ArchiveSegment, 0, len(a.segments))
	for _, segment := range a.segments {
		file, err := os.Open(filepath.Join(a.dir, segment.name))
		if err != nil {
			closeArchiveSegments(result)
			return nil, fmt.Errorf("open resource archive snapshot: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			closeArchiveSegments(result)
			return nil, fmt.Errorf("stat resource archive snapshot: %w", err)
		}
		result = append(result, ArchiveSegment{Name: segment.name, Size: info.Size(), File: file})
	}
	return result, nil
}

// CloseArchiveSegments closes all files returned by OpenSnapshot.
func CloseArchiveSegments(segments []ArchiveSegment) {
	closeArchiveSegments(segments)
}

func closeArchiveSegments(segments []ArchiveSegment) {
	for _, segment := range segments {
		_ = segment.File.Close()
	}
}

// Close stops accepting samples and waits for the writer to flush its bounded queue.
func (a *Archive) Close(ctx context.Context) error {
	if ctx == nil {
		return errors.New("resource archive close context is required")
	}
	a.queueMu.Lock()
	if !a.closed {
		a.closed = true
		close(a.queue)
	}
	a.queueMu.Unlock()
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Archive) run() {
	ticker := time.NewTicker(archiveFlushPeriod)
	defer ticker.Stop()
	defer close(a.done)
	for {
		select {
		case sample, ok := <-a.queue:
			if !ok {
				a.flushAndClose()
				return
			}
			if !a.failed.Load() {
				if err := a.write(sample); err != nil {
					a.fail(err)
				}
			} else {
				a.dropped.Add(1)
			}
		case <-ticker.C:
			a.reportDrops()
			if !a.failed.Load() {
				a.fileMu.Lock()
				err := a.writer.Flush()
				a.fileMu.Unlock()
				if err != nil {
					a.fail(err)
				}
			}
		}
	}
}

func (a *Archive) write(sample Snapshot) error {
	data, err := json.Marshal(sample)
	if err != nil {
		return fmt.Errorf("encode resource archive sample: %w", err)
	}
	data = append(data, '\n')
	if int64(len(data)) > a.maxSegmentBytes {
		return errors.New("resource archive sample exceeds segment size limit")
	}
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	if a.fileSize+int64(len(data)) > a.maxSegmentBytes {
		if err := a.rotateLocked(); err != nil {
			return err
		}
	}
	written, err := a.writer.Write(data)
	a.fileSize += int64(written)
	if err != nil {
		return fmt.Errorf("write resource archive sample: %w", err)
	}
	return nil
}

func (a *Archive) rotateLocked() error {
	if a.writer != nil {
		if err := a.writer.Flush(); err != nil {
			return fmt.Errorf("flush resource archive segment: %w", err)
		}
		a.closeFileLocked()
	}
	for len(a.segments) >= a.maxSegments {
		if err := a.removeOldestLocked(); err != nil {
			return err
		}
	}
	sequence := a.nextSequence
	a.nextSequence++
	name := fmt.Sprintf("resource-%020d.jsonl", sequence)
	file, err := os.OpenFile(filepath.Join(a.dir, name), os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create resource archive segment: %w", err)
	}
	a.file, a.writer, a.fileSize = file, bufio.NewWriterSize(file, 16<<10), 0
	a.segments = append(a.segments, archiveSegment{sequence: sequence, name: name})
	return nil
}

func (a *Archive) removeOldestLocked() error {
	oldest := a.segments[0]
	if err := os.Remove(filepath.Join(a.dir, oldest.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove expired resource archive segment: %w", err)
	}
	a.segments = a.segments[1:]
	return nil
}

func (a *Archive) fail(err error) {
	if a.failed.Swap(true) {
		return
	}
	a.logger.Error("resource archive disabled after a write failure", "service", "ara-mcp", "component", "resource_archive", "event", "archive_write_failed", "error_class", "write_error")
	a.fileMu.Lock()
	a.closeFileLocked()
	a.fileMu.Unlock()
	_ = err // The raw filesystem error may contain a local path; retain only the bounded class.
}

func (a *Archive) flushAndClose() {
	a.fileMu.Lock()
	defer a.fileMu.Unlock()
	a.reportDrops()
	if a.writer != nil {
		if err := a.writer.Flush(); err != nil {
			a.failed.Store(true)
			a.logger.Error("resource archive flush failed", "service", "ara-mcp", "component", "resource_archive", "event", "archive_write_failed", "error_class", "write_error")
		}
	}
	a.closeFileLocked()
}

func (a *Archive) reportDrops() {
	if dropped := a.dropped.Load(); dropped > a.reportedDropped.Load() {
		previous := a.reportedDropped.Swap(dropped)
		a.logger.Warn("resource archive dropped samples", "service", "ara-mcp", "component", "resource_archive", "event", "archive_samples_dropped", "count", dropped-previous)
	}
}

func (a *Archive) closeFileLocked() {
	if a.file != nil {
		_ = a.file.Close()
		a.file, a.writer = nil, nil
	}
}
