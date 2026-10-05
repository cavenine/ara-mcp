// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcpserver

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/cavenine/ara-mcp/internal/ara"
)

func TestLiveAraBeginControlRequiresProfileWithoutClaimingSlot(t *testing.T) {
	baseURL := os.Getenv("ARA_MCP_LIVE_ARA_URL")
	if baseURL == "" {
		t.Skip("set ARA_MCP_LIVE_ARA_URL to run the opt-in Ara control check")
	}
	client, err := ara.New(ara.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		CurrentProfileID *string `json:"current_profile_id"`
	}
	if _, err := client.Do(t.Context(), ara.Request{Method: http.MethodGet, Route: "/server/state"}, &state); err != nil {
		t.Fatal(err)
	}
	if state.CurrentProfileID != nil && *state.CurrentProfileID != "" {
		t.Skip("Ara has an active profile; leaving the configured rig untouched")
	}
	var session struct {
		Connected bool `json:"connected"`
	}
	if _, err := client.Do(t.Context(), ara.Request{Method: http.MethodGet, Route: "/server/session"}, &session); err != nil {
		t.Fatal(err)
	}
	if session.Connected {
		t.Skip("Ara already has a control-session owner; leaving it undisturbed")
	}
	control, err := NewControlManager(client, nil, "integration", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close(context.Background(), "")
	if _, err := control.Begin(t.Context(), "live-control-profile-check"); err == nil || err.Error() != "begin control requires a configured active Ara profile" {
		t.Fatalf("Begin() error = %v, want active-profile prerequisite rejection", err)
	}
	var after struct {
		Connected bool `json:"connected"`
	}
	if _, err := client.Do(t.Context(), ara.Request{Method: http.MethodGet, Route: "/server/session"}, &after); err != nil {
		t.Fatal(err)
	}
	if after.Connected {
		t.Fatal("begin_control claimed Ara's slot without an active profile")
	}
}
