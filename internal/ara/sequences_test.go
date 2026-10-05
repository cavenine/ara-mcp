// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSequenceExecutionRequestsUseAraRoutesAndPreserveAcceptance(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	tests := []struct {
		name  string
		path  string
		call  func(*Client) (Result, error)
		body  string
		start bool
	}{
		{
			name: "start",
			path: "/api/v1/sequences/" + sequenceID + "/start",
			call: func(client *Client) (Result, error) {
				return client.StartSequenceWithRequestID(t.Context(), sequenceID, "request-01")
			},
			start: true,
		},
		{
			name: "pause",
			path: "/api/v1/sequences/" + sequenceID + "/pause",
			call: func(client *Client) (Result, error) {
				return client.PauseSequenceWithRequestID(t.Context(), sequenceID, "request-01")
			},
		},
		{
			name: "resume",
			path: "/api/v1/sequences/" + sequenceID + "/resume",
			body: `{"recenter":false,"refocus":false}`,
			call: func(client *Client) (Result, error) {
				return client.ResumeSequenceWithRequestID(t.Context(), sequenceID, "request-01")
			},
		},
		{
			name: "stop",
			path: "/api/v1/sequences/" + sequenceID + "/stop",
			call: func(client *Client) (Result, error) {
				return client.StopSequenceWithRequestID(t.Context(), sequenceID, "request-01")
			},
		},
		{
			name: "abort",
			path: "/api/v1/sequences/" + sequenceID + "/abort",
			call: func(client *Client) (Result, error) {
				return client.AbortSequenceWithRequestID(t.Context(), sequenceID, "request-01")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != test.path {
					t.Errorf("request = %s %s, want POST %s", r.Method, r.URL.Path, test.path)
				}
				if got := r.Header.Get("X-Request-ID"); got != "request-01" {
					t.Errorf("request ID = %q, want request-01", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read body: %v", err)
				}
				if test.start {
					var fields map[string]any
					if err := json.Unmarshal(body, &fields); err != nil {
						t.Errorf("decode start fields: %v", err)
					}
					if len(fields) != 3 || fields["dry_run"] != false || fields["start_from_instruction_index"] != nil || fields["continue_on_recoverable_errors"] != false {
						t.Errorf("start fields = %v, want all three fixed Ara options", fields)
					}
				} else if string(body) != test.body {
					t.Errorf("run-control body = %s, want %s", body, test.body)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(w, `{"operation_id":"receipt-01"}`)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			result, err := test.call(client)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != OutcomeAccepted || result.ReceiptID != "receipt-01" || result.RetrySafe {
				t.Fatalf("result = %+v, want accepted receipt with no retry guarantee", result)
			}
		})
	}
}

func TestGetSequenceStatePreservesAraState(t *testing.T) {
	const (
		sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
		state      = `{"sequence_id":"` + sequenceID + `","run_id":"run-01","state":"running","current_instruction_index":2,"frames_captured":1}`
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/sequences/"+sequenceID+"/state" {
			t.Errorf("request = %s %s, want sequence state GET", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Request-ID"); got != "request-02" {
			t.Errorf("request ID = %q, want request-02", got)
		}
		_, _ = io.WriteString(w, state)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := client.GetSequenceStateWithRequestID(t.Context(), sequenceID, "request-02")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != state {
		t.Fatalf("state = %s, want exact Ara state %s", got, state)
	}
}

func TestDeleteSequenceUsesAraDeleteRoute(t *testing.T) {
	const sequenceID = "7db1118d-57d9-4657-9779-8e1e619b12c4"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/sequences/"+sequenceID {
			t.Errorf("request = %s %s, want DELETE saved sequence", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Request-ID"); got != "delete-sequence-01" {
			t.Errorf("request ID = %q, want delete-sequence-01", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DeleteSequenceWithRequestID(t.Context(), sequenceID, "delete-sequence-01")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeCompleted || result.Status != http.StatusNoContent {
		t.Fatalf("delete result = %+v, want completed HTTP 204", result)
	}
}
