// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGuideFocusStartPreservesAra400And409WithoutRetry(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
	}{
		{name: "invalid exposure", status: http.StatusBadRequest},
		{name: "lease conflict", status: http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				http.Error(w, `{"title":"rejected"}`, test.status)
			}))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, ReadRetries: 2})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.StartGuideFocusWithRequestID(t.Context(), GuideFocusStartRequest{ExposureSec: 2}, "focus-start")
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Status != test.status || result.Outcome != OutcomeFailed {
				t.Fatalf("start = %+v, %v; want Ara HTTP %d", result, err, test.status)
			}
			if requests != 1 {
				t.Fatalf("start requests = %d, want no mutation retry", requests)
			}
		})
	}
}

func TestGuideFocusRoutesAndFrameBounds(t *testing.T) {
	frame := []byte{0xff, 0xd8, 1, 2, 0xff, 0xd9}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/equipment/guider/focus/start":
			if r.Method != http.MethodPost {
				t.Errorf("start method = %s", r.Method)
			}
			body, _ := io.ReadAll(r.Body)
			if string(body) != `{"exposure_sec":2.5,"binning":2}` {
				t.Errorf("start body = %s", body)
			}
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/equipment/guider/focus/stop":
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/equipment/guider/focus":
			_, _ = io.WriteString(w, `{"active":true,"state":"running","exposure_sec":2.5,"seq":3,"recent":[],"consecutive_failures":0,"has_frame":true}`)
		case "/api/v1/equipment/guider/focus/frame":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(frame)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	started, err := client.StartGuideFocusWithRequestID(t.Context(), GuideFocusStartRequest{ExposureSec: 2.5, Binning: new(2)}, "start-01")
	if err != nil || started.Outcome != OutcomeAccepted {
		t.Fatalf("start = %+v, %v", started, err)
	}
	status, _, err := client.GetGuideFocusStatusWithRequestID(t.Context(), "status-01")
	if err != nil || !status.Active || status.Seq != 3 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	got, frameResult, err := client.GetGuideFocusFrameWithRequestID(t.Context(), "frame-01")
	if err != nil || frameResult.Status != http.StatusOK || string(got) != string(frame) {
		t.Fatalf("frame = %v, %+v, %v", got, frameResult, err)
	}
	stopped, err := client.StopGuideFocusWithRequestID(t.Context(), "stop-01")
	if err != nil || stopped.Outcome != OutcomeCompleted {
		t.Fatalf("stop = %+v, %v", stopped, err)
	}
}

func TestGuideFocusFrameNoContentAndOversize(t *testing.T) {
	var tooLarge bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if tooLarge {
			_, _ = w.Write(make([]byte, 1<<20+1))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	frame, result, err := client.GetGuideFocusFrameWithRequestID(t.Context(), "frame-01")
	if err != nil || result.Status != http.StatusNoContent || frame != nil {
		t.Fatalf("no frame = %d bytes, %+v, %v", len(frame), result, err)
	}
	tooLarge = true
	if _, _, err := client.GetGuideFocusFrameWithRequestID(t.Context(), "frame-02"); err == nil {
		t.Fatal("oversized frame succeeded")
	}
}
