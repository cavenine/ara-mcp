// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestStdioProcessWritesOnlyMCPFramesToStdout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ara-mcp")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stdio-smoke","version":"1"}}}` + "\n"
	command := exec.CommandContext(t.Context(), binary, "--ara-url", "http://127.0.0.1:1", "serve")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(stdin, initialize); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		stderrBytes, _ := io.ReadAll(stderr)
		t.Fatalf("process emitted no MCP frame: %v; stderr=%s", scanner.Err(), stderrBytes)
	}
	var frames [][]byte
	frames = append(frames, bytes.Clone(scanner.Bytes()))
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	for scanner.Scan() {
		frames = append(frames, bytes.Clone(scanner.Bytes()))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	stderrBytes, err := io.ReadAll(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("stdio server exited with %v; stderr=%s", err, stderrBytes)
	}
	for _, line := range frames {
		var frame struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal(line, &frame); err != nil || frame.JSONRPC != "2.0" {
			t.Fatalf("stdout contains non-MCP output %q (decode error %v)", line, err)
		}
	}
	for _, line := range bytes.Split(bytes.TrimSpace(stderrBytes), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil || record["level"] == nil {
			t.Fatalf("stderr contains non-JSON diagnostic %q (decode error %v)", line, err)
		}
	}
}
