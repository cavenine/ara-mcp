// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServeHTTPStartsAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Config{
			AraURL: "http://127.0.0.1:5555", Transport: "http", LogLevel: "error", Timeout: time.Second,
			HTTPListen: address, HTTPBearerToken: "0123456789abcdef0123456789abcdef",
		}, "test", io.Discard)
	}()
	t.Cleanup(cancel)

	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var response *http.Response
	for response == nil {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+address+"/mcp", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer 0123456789abcdef0123456789abcdef")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err = http.DefaultClient.Do(request)
		if err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatalf("HTTP listener did not start: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid MCP request status = %d, want %d", response.StatusCode, http.StatusBadRequest)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("HTTP service did not shut down after cancellation")
	}
}
