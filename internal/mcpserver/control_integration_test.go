// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcpserver

import (
	"context"
	"encoding/json/v2"
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
	stateBody, _, err := client.GetServerStateWithRequestID(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		CurrentProfileID *string `json:"current_profile_id"`
	}
	if err := json.Unmarshal(stateBody, &state); err != nil {
		t.Fatal(err)
	}
	if state.CurrentProfileID != nil && *state.CurrentProfileID != "" {
		t.Skip("Ara has an active profile; leaving the configured rig untouched")
	}
	session, _, err := client.GetServerSessionWithRequestID(t.Context(), "")
	if err != nil {
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
	after, _, err := client.GetServerSessionWithRequestID(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Connected {
		t.Fatal("begin_control claimed Ara's slot without an active profile")
	}
}
