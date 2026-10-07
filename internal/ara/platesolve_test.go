// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSolveFrameUsesAraContract(t *testing.T) {
	const frameID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/platesolve/frames/"+frameID+"/solve" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"ra":12.5,"dec":-30,"orientation":15,"pixel_scale":1.2,"search_radius":2}`))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, result, err := client.SolveFrameWithRequestID(t.Context(), frameID, nil, "req")
	if err != nil || result.Status != http.StatusOK || !got.Success || got.RA == nil || *got.RA != 12.5 || got.Dec == nil || *got.Dec != -30 || got.Orientation == nil || *got.Orientation != 15 || got.PixelScale == nil || *got.PixelScale != 1.2 || got.SearchRadius == nil || *got.SearchRadius != 2 {
		t.Fatalf("solve = %+v, result %+v, err %v", got, result, err)
	}
}

func TestCenterAndCancelJobUseAraContracts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/platesolve/center":
			if r.Header.Get("X-Request-ID") != "req" {
				t.Errorf("request ID = %q", r.Header.Get("X-Request-ID"))
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"job_id":"job-1","job_type":"center","state":"queued","done":0,"total":3}`))
		case "DELETE /api/v1/jobs/job-1":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	job, result, err := client.CenterWithRequestID(t.Context(), 12.5, -30, "req")
	if err != nil || result.Outcome != OutcomeAccepted || job.JobID != "job-1" || job.JobType != "center" {
		t.Fatalf("center = %+v, %+v, %v", job, result, err)
	}
	result, err = client.CancelJobWithRequestID(t.Context(), "job-1", "req")
	if err != nil || result.Status != http.StatusNoContent {
		t.Fatalf("cancel = %+v, %v", result, err)
	}
}
