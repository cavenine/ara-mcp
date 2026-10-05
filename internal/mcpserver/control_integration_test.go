// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build integration

package mcpserver

import (
	"context"
	"io"
	"log/slog"
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
	profiles, _, err := client.ListProfilesWithRequestID(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if profiles.ActiveID != nil && *profiles.ActiveID != "" {
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

func TestLiveAraBeginControlUsesProfileRepositorySelection(t *testing.T) {
	baseURL := os.Getenv("ARA_MCP_LIVE_ARA_URL")
	if baseURL == "" {
		t.Skip("set ARA_MCP_LIVE_ARA_URL to run the opt-in Ara control check")
	}
	client, err := ara.New(ara.Config{BaseURL: baseURL})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _, err := client.ListProfilesWithRequestID(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if profiles.ActiveID == nil || *profiles.ActiveID == "" {
		t.Skip("Ara has no active profile to exercise")
	}
	session, _, err := client.GetServerSessionWithRequestID(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if session.Connected {
		t.Skip("Ara already has a control-session owner; leaving it undisturbed")
	}
	control, err := NewControlManager(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "integration", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := control.Close(context.Background(), "live-control-profile-cleanup"); err != nil {
			t.Errorf("release Ara control session: %v", err)
		}
	})
	result, err := control.Begin(t.Context(), "live-control-profile-selection")
	if err != nil {
		t.Fatalf("Begin() with profile repository selection: %v", err)
	}
	if result.ControlID == "" || control.Snapshot().ControlOwnership != "owned" {
		t.Fatalf("control result = %+v, status = %+v; want active ownership", result, control.Snapshot())
	}
	if err := control.End(t.Context(), result.ControlID, "live-control-profile-selection-end"); err != nil {
		t.Fatalf("End() control session: %v", err)
	}
}
