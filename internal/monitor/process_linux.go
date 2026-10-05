// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package monitor

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func readProcessStats() (processStats, error) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return processStats{}, err
	}
	closingParen := strings.LastIndexByte(string(stat), ')')
	if closingParen < 0 {
		return processStats{}, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(stat[closingParen+1:]))
	if len(fields) <= 12 {
		return processStats{}, fmt.Errorf("incomplete process stat")
	}
	userTicks, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return processStats{}, fmt.Errorf("parse process user time: %w", err)
	}
	systemTicks, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return processStats{}, fmt.Errorf("parse process system time: %w", err)
	}
	statm, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return processStats{}, err
	}
	pages := strings.Fields(string(statm))
	if len(pages) < 2 {
		return processStats{}, fmt.Errorf("incomplete process statm")
	}
	residentPages, err := strconv.ParseUint(pages[1], 10, 64)
	if err != nil {
		return processStats{}, fmt.Errorf("parse resident pages: %w", err)
	}
	// ponytail: Linux USER_HZ is 100 on the supported ARM64/amd64 targets; use a platform library if that target set changes.
	cpu := time.Duration(userTicks+systemTicks) * 10 * time.Millisecond
	return processStats{cpu: cpu, rss: residentPages * uint64(os.Getpagesize())}, nil
}
