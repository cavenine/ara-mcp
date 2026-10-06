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

func TestStartExposureUsesAraContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/equipment/camera/exposure" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"exposure_sec":2.5,"gain":0,"bin_x":1,"bin_y":1,"filter_name":"L"}` {
			t.Errorf("body = %s", body)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"frame_id":"frame-01","preview_url":"/api/v1/frames/frame-01/preview","exposure_sec":2.5,"captured_at":"2026-10-05T00:00:00Z"}`)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, outcome, err := client.StartExposureWithRequestID(t.Context(), ExposureRequest{
		ExposureSec: 2.5, Gain: new(0), BinX: 1, BinY: 1, FilterName: "L",
	}, "request-01")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Outcome != OutcomeAccepted || result.FrameID != "frame-01" || result.ExposureSec != 2.5 {
		t.Fatalf("result = %+v, outcome = %+v", result, outcome)
	}
}

func TestManualEquipmentRoutesUseAraContracts(t *testing.T) {
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts[r.URL.Path]++
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		switch r.URL.Path {
		case "/api/v1/equipment/camera/exposure/abort", "/api/v1/equipment/camera/cooler", "/api/v1/equipment/telescope/slew", "/api/v1/equipment/telescope/park", "/api/v1/equipment/telescope/unpark", "/api/v1/equipment/telescope/abort", "/api/v1/equipment/focuser/move", "/api/v1/equipment/filterwheel/change":
			w.WriteHeader(http.StatusAccepted)
		case "/api/v1/equipment/focuser/autofocus":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"job_id":"job-01","job_type":"autofocus","state":"queued","done":0,"total":9,"started_utc":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/server/emergency-stop":
			_, _ = io.WriteString(w, `{"already_in_progress":false,"runs_aborted":1,"exposure_aborted":true,"guiding_stopped":false,"park_requested":true,"flat_panel_light_off":false,"failed_rungs":[]}`)
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		call func() error
	}{
		{"abort exposure", func() error {
			result, err := client.AbortExposureWithRequestID(t.Context(), "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"cooler", func() error {
			result, err := client.SetCameraCoolerWithRequestID(t.Context(), true, nil, "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"slew", func() error {
			result, err := client.SlewTelescopeWithRequestID(t.Context(), 12.5, -20, false, "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"park", func() error {
			result, err := client.ParkTelescopeWithRequestID(t.Context(), "test", "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"unpark", func() error {
			result, err := client.UnparkTelescopeWithRequestID(t.Context(), "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"abort slew", func() error {
			result, err := client.AbortTelescopeSlewWithRequestID(t.Context(), "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"focuser", func() error {
			result, err := client.MoveFocuserWithRequestID(t.Context(), 100, false, "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"autofocus", func() error {
			job, result, err := client.RunAutofocusWithRequestID(t.Context(), "r1")
			if err == nil && (job.JobID != "job-01" || result.Outcome != OutcomeAccepted) {
				return errors.New("autofocus response lost its job acceptance")
			}
			return err
		}},
		{"filter", func() error {
			result, err := client.SelectFilterWithRequestID(t.Context(), 2, "r1")
			return requireOutcome(result, err, OutcomeAccepted)
		}},
		{"emergency stop", func() error {
			stop, result, err := client.EmergencyStopWithRequestID(t.Context(), "r1")
			if err == nil && (result.Outcome != OutcomeCompleted || stop.RunsAborted != 1 || !stop.ParkRequested) {
				return errors.New("emergency stop response lost rung results")
			}
			return err
		}},
	}
	routes := []string{"/api/v1/equipment/camera/exposure/abort", "/api/v1/equipment/camera/cooler", "/api/v1/equipment/telescope/slew", "/api/v1/equipment/telescope/park", "/api/v1/equipment/telescope/unpark", "/api/v1/equipment/telescope/abort", "/api/v1/equipment/focuser/move", "/api/v1/equipment/focuser/autofocus", "/api/v1/equipment/filterwheel/change", "/api/v1/server/emergency-stop"}
	for i, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err != nil {
				t.Fatal(err)
			}
			if counts[routes[i]] != 1 {
				t.Fatalf("requests to %s = %d, want 1", routes[i], counts[routes[i]])
			}
		})
	}
}

func TestDisablingCameraCoolerOmitsIgnoredSetpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != `{"enabled":false}` {
			t.Errorf("cooler body = %s, want disabled without setpoint", body)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	target := -10.0
	result, err := client.SetCameraCoolerWithRequestID(t.Context(), false, &target, "request-01")
	if err != nil || result.Outcome != OutcomeAccepted {
		t.Fatalf("cooler result = %+v, error = %v", result, err)
	}
}

func TestDitherGuiderUsesAraPixelQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/equipment/guider/dither" || r.URL.Query().Get("pixels") != "2.5" {
			t.Errorf("request = %s %s, want POST guider dither pixels=2.5", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, `{"operation_id":"guide-op-01","operation_type":"guider.dither","accepted_utc":"2026-10-05T00:00:00Z"}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.DitherGuiderWithRequestID(t.Context(), 2.5, "request-01")
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeAccepted || result.ReceiptID != "guide-op-01" {
		t.Fatalf("result = %+v", result)
	}
}

func TestGetJobStatusUsesAraContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/jobs/job-01" || r.Header.Get("X-Request-ID") != "request-01" {
			t.Errorf("request = %s %s, request ID %q", r.Method, r.URL.Path, r.Header.Get("X-Request-ID"))
		}
		_, _ = io.WriteString(w, `{"job_id":"job-01","job_type":"autofocus","state":"running","done":3,"total":9,"started_utc":"2026-10-05T00:00:00Z"}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	job, _, err := client.GetJobWithRequestID(t.Context(), "job-01", "request-01")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "running" || job.Done != 3 || job.Total != 9 {
		t.Fatalf("job = %+v", job)
	}
}

func TestFramePreviewReturnsBoundedImageBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/frames/frame-01/thumbnail" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	image, _, err := client.GetFrameThumbnailWithRequestID(t.Context(), "frame-01", "request-02")
	if err != nil {
		t.Fatal(err)
	}
	if string(image) != string([]byte{0xff, 0xd8, 0xff, 0xd9}) {
		t.Fatalf("image bytes = %v", image)
	}
}

func TestFrameThumbnailRejectsResponseAboveOneMiB(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, maxPreviewBytes+1))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.GetFrameThumbnailWithRequestID(t.Context(), "frame-01", "request-03"); err == nil {
		t.Fatal("oversized preview returned without error")
	}
}

func TestFrameCatalogUsesAraCursorAndDetailRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/frames":
			if r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("cursor") != "next/page" {
				t.Errorf("frame query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"items":[{"id":"frame-01","target_name":"M31"}],"next_cursor":null,"has_more":false}`)
		case "/api/v1/frames/frame-01":
			_, _ = io.WriteString(w, `{"id":"frame-01","file_size_bytes":42,"width":800,"height":600}`)
		default:
			t.Errorf("unexpected route: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	page, _, err := client.ListFramesWithRequestID(t.Context(), 3, "next/page", "request-04")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "frame-01" {
		t.Fatalf("frame page = %+v", page)
	}
	frame, _, err := client.GetFrameWithRequestID(t.Context(), "frame-01", "request-05")
	if err != nil || frame.ID != "frame-01" || frame.Width != 800 {
		t.Fatalf("frame = %+v, error = %v", frame, err)
	}
}

func TestFrameListRejectsUnboundedPageParameters(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:5555"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ListFramesWithRequestID(t.Context(), 101, "", "request-06")
	var requestError *RequestError
	if !errors.As(err, &requestError) || requestError.Class != "invalid_request" {
		t.Fatalf("error = %v, want invalid frame-list request", err)
	}
}

func requireOutcome(result Result, err error, want Outcome) error {
	if err != nil {
		return err
	}
	if result.Outcome != want {
		return errors.New("Ara request returned unexpected outcome")
	}
	return nil
}
