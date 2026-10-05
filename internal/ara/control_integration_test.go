// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package ara

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestLiveAraControlSessionAndHeartbeat(t *testing.T) {
	baseURL := os.Getenv("ARA_MCP_LIVE_ARA_URL")
	if baseURL == "" {
		t.Skip("set ARA_MCP_LIVE_ARA_URL to run the opt-in Ara session check")
	}
	client, err := New(Config{BaseURL: baseURL, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	var current struct {
		Connected bool `json:"connected"`
	}
	if _, err := client.do(t.Context(), request{Method: http.MethodGet, Route: "/server/session"}, &current); err != nil {
		t.Fatal(err)
	}
	if current.Connected {
		t.Skip("Ara already has a control-session owner; leaving it undisturbed")
	}
	var state struct {
		ResumeToken string `json:"ws_resume_token"`
	}
	if _, err := client.do(t.Context(), request{Method: http.MethodGet, Route: "/server/state"}, &state); err != nil {
		t.Fatal(err)
	}
	session, _, err := client.ConnectWithRequestID(t.Context(), "ara-mcp-live-check", nil, "live-control-check")
	if err != nil {
		t.Fatal(err)
	}

	var connection *websocket.Conn
	var stop context.CancelFunc
	done := make(chan error, 1)
	maintaining := false
	defer func() {
		if stop != nil {
			stop()
		}
		if connection != nil {
			_ = connection.CloseNow()
		}
		if maintaining {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("Ara control WebSocket reader did not stop")
			}
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := client.DisconnectWithRequestID(cleanupContext, session, "live-control-check-end"); err != nil {
			t.Errorf("release live Ara control session: %v", err)
		}
	}()

	connection, err = client.OpenControlWebSocket(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ResumeControlWebSocket(t.Context(), connection, state.ResumeToken); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	stop = cancel
	heartbeats := make(chan time.Time, 1)
	maintaining = true
	go func() {
		done <- client.MaintainControlWebSocket(ctx, connection, func(at time.Time) {
			select {
			case heartbeats <- at:
			default:
			}
		}, nil)
	}()
	select {
	case heartbeat := <-heartbeats:
		if heartbeat.IsZero() {
			t.Fatal("Ara heartbeat timestamp is zero")
		}
	case <-ctx.Done():
		t.Fatalf("Ara heartbeat not received before deadline: %v", ctx.Err())
	}
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Ara control WebSocket reader did not stop after cancellation")
	}
	maintaining = false
	connection = nil

	priorSessionID := session.SessionID()
	session, _, err = client.ConnectWithRequestID(t.Context(), "ara-mcp-live-check", &priorSessionID, "live-control-reclaim")
	if err != nil {
		t.Fatalf("reclaim same Ara session: %v", err)
	}
	if session.SessionID() != priorSessionID {
		t.Fatal("Ara returned a different session capability during re-claim")
	}
	if _, err := client.do(t.Context(), request{Method: http.MethodGet, Route: "/server/state"}, &state); err != nil {
		t.Fatal(err)
	}
	connection, err = client.OpenControlWebSocket(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ResumeControlWebSocket(t.Context(), connection, state.ResumeToken); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
	stop = cancel
	done = make(chan error, 1)
	maintaining = true
	go func() {
		done <- client.MaintainControlWebSocket(ctx, connection, nil, nil)
	}()
	humanClient, err := New(Config{BaseURL: baseURL, Timeout: 4 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = humanClient.ConnectWithRequestID(ctx, "ara-mcp-handoff-probe", nil, "live-handoff-probe")
	apiError, rejected := errors.AsType[*APIError](err)
	if !rejected || apiError.Status != http.StatusConflict {
		t.Fatalf("competing live claim error = %v, want HTTP 409 after bound adapter rejects takeover", err)
	}
	stop()
	_ = connection.CloseNow()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reclaimed Ara control WebSocket reader did not stop")
	}
	maintaining = false
	connection = nil
}
