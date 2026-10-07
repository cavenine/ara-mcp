// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestListFaultsUsesBoundedAraCursorAndFilters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/faults" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		for key, want := range map[string]string{"limit": "200", "cursor": "400", "equipmentType": "rotator", "sessionId": "b15e5138-12f0-4c41-8a43-ed79ef527e12", "unresolvedOnly": "true", "faultType": "disconnected"} {
			if got := r.URL.Query().Get(key); got != want {
				t.Errorf("query %s = %q, want %q", key, got, want)
			}
		}
		_, _ = io.WriteString(w, `{"items":[{"id":"b15e5138-12f0-4c41-8a43-ed79ef527e12","detected_utc":"2026-10-06T00:00:00Z","equipment_type":"camera","fault_type":"disconnected","affected_frames":[]}],"next_cursor":"600","has_more":true}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	page, result, err := client.ListFaultsWithRequestID(t.Context(), FaultListParams{
		Limit: 200, Cursor: "400", EquipmentType: "rotator", SessionID: "b15e5138-12f0-4c41-8a43-ed79ef527e12",
		UnresolvedOnly: new(true), FaultType: "disconnected",
	}, "fault-list-01")
	if err != nil || result.Outcome != OutcomeCompleted || !page.HasMore || page.NextCursor == nil || *page.NextCursor != "600" || len(page.Items) != 1 {
		t.Fatalf("page=%+v result=%+v error=%v", page, result, err)
	}
}

func TestListFaultsRejectsInvalidCursorAndFiltersBeforeDispatch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []FaultListParams{
		{Cursor: "bad"}, {Cursor: "-1"}, {Cursor: "2147483648"}, {EquipmentType: "bad type"}, {FaultType: "bad type"},
	} {
		if _, _, err := client.ListFaultsWithRequestID(t.Context(), params, ""); err == nil {
			t.Errorf("ListFaults(%+v) error = nil", params)
		}
	}
	if requests != 0 {
		t.Fatalf("upstream requests = %d, want 0", requests)
	}
}

func TestListFaultsRejectsOutOfRangeLimitAndMalformedResponseCursor(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"items":[],"next_cursor":"not-an-offset","has_more":true}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 201, 500} {
		if _, _, err := client.ListFaultsWithRequestID(t.Context(), FaultListParams{Limit: limit}, ""); err == nil {
			t.Errorf("limit %d unexpectedly succeeded", limit)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid limits dispatched %d requests", requests.Load())
	}
	_, result, err := client.ListFaultsWithRequestID(t.Context(), FaultListParams{Limit: 100}, "")
	var requestError *RequestError
	if !errors.As(err, &requestError) || requestError.Class != "decode" || result.Outcome != OutcomeFailed {
		t.Fatalf("result=%+v error=%v, want failed response-cursor decode", result, err)
	}
}

func TestGetFaultUsesGuidRoute(t *testing.T) {
	const id = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/faults/"+id {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"`+id+`","detected_utc":"2026-10-06T00:00:00Z","equipment_type":"camera","fault_type":"disconnected","affected_frames":[]}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	fault, result, err := client.GetFaultWithRequestID(t.Context(), id, "fault-detail-01")
	if err != nil || result.Outcome != OutcomeCompleted || fault.ID != id {
		t.Fatalf("fault=%+v result=%+v error=%v", fault, result, err)
	}
}
