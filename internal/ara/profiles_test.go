// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListProfilesReadsActiveProfileAndRequestCorrelation(t *testing.T) {
	const profileID = "e1d64755-e2ae-46f1-aa43-c6e67419e1e9"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/profiles" {
			t.Errorf("request = %s %s, want GET /api/v1/profiles", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Request-ID"); got != "profile-check-01" {
			t.Errorf("request ID = %q, want profile-check-01", got)
		}
		_, _ = io.WriteString(w, `{"active_id":"`+profileID+`","profiles":[{"id":"`+profileID+`","name":"ara-mcp-t06-omnisim","created_utc":"2026-10-05T19:43:06Z","updated_utc":"2026-10-05T19:43:06Z"}]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	profiles, _, err := client.ListProfilesWithRequestID(t.Context(), "profile-check-01")
	if err != nil {
		t.Fatal(err)
	}
	if profiles.ActiveID == nil || *profiles.ActiveID != profileID || len(profiles.Profiles) != 1 || profiles.Profiles[0].Name != "ara-mcp-t06-omnisim" {
		t.Fatalf("profiles = %+v, want selected test profile %s", profiles, profileID)
	}
}
