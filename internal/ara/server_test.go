// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetProfileStorageWithRequestID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/profile/storage" {
			t.Errorf("request = %s %s, want profile storage GET", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Request-ID"); got != "storage-check-01" {
			t.Errorf("request ID = %q, want storage-check-01", got)
		}
		_, _ = io.WriteString(w, `{"save_directory":"/tmp/ara-mcp-t06-captures"}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	storage, _, err := client.GetProfileStorageWithRequestID(t.Context(), "storage-check-01")
	if err != nil {
		t.Fatal(err)
	}
	if string(storage) != `{"save_directory":"/tmp/ara-mcp-t06-captures"}` {
		t.Fatalf("profile storage = %s, want exact Ara JSON", storage)
	}
}
