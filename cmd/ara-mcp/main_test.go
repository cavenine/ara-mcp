// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStdioProcessWritesOnlyMCPFramesToStdout(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "ara-mcp")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build executable: %v\n%s", err, output)
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"stdio-smoke","version":"1"}}}` + "\n"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	diagnosticsAddress := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), binary, "--ara-url", "http://127.0.0.1:1", "serve")
	command.Env = append(os.Environ(), "ARA_MCP_DIAGNOSTICS_LISTEN="+diagnosticsAddress)
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, requestErr := (&http.Client{Timeout: 100 * time.Millisecond}).Get("http://" + diagnosticsAddress + "/healthz")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("diagnostics /healthz status = %d", response.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("diagnostics listener did not start: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
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
