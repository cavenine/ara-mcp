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

func TestGetAutofocusStateWithRequestIDUsesAraContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/autofocus/state" {
			t.Errorf("Ara request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"state":"running","mode":"classic","phase":"sweep","trigger":"manual","started_utc":"2026-10-06T16:00:00Z","total_steps":9,"completed_steps":4,"probes":[{"index":4,"phase":"fine","position":1200,"hfr":2.35,"stars":87,"kept":true}],"fit":{"algorithm":"parabola","r_squared":0.98,"best_position":1201.5,"predicted_hfr":2.1,"within_sampled_range":true,"curve":[{"position":1200,"hfr":2.35}]},"has_frame":true,"frame_seq":7,"frame_position":1200,"frame_hfr":2.35}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := client.GetAutofocusStateWithRequestID(t.Context(), "test-request")
	if err != nil {
		t.Fatal(err)
	}
	if run.State != "running" || run.Mode == nil || *run.Mode != "classic" || run.CompletedSteps != 4 || len(run.Probes) != 1 || run.Probes[0].Position != 1200 || run.Fit == nil || run.Fit.Algorithm != "parabola" || !run.HasFrame || run.FrameSeq != 7 {
		t.Fatalf("autofocus state = %+v", run)
	}
}

func TestGetAutofocusFrameWithRequestIDBoundsJPEGAndHandlesNoFrame(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/autofocus/frame" {
			t.Errorf("Ara request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch calls {
		case 1:
			w.Header().Set("Content-Type", "image/jpeg")
			w.Header().Set("X-Frame-Seq", "7")
			_, _ = w.Write([]byte{0xff, 0xd8, 0xff, 0xd9})
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(make([]byte, maxPreviewBytes+1))
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	image, frameSeq, _, err := client.GetAutofocusFrameWithRequestID(t.Context(), "test-request")
	if err != nil || len(image) != 4 || image[0] != 0xff || frameSeq != 7 {
		t.Fatalf("autofocus frame = %d bytes, seq=%d, error = %v", len(image), frameSeq, err)
	}
	image, frameSeq, result, err := client.GetAutofocusFrameWithRequestID(t.Context(), "test-request")
	if err != nil || len(image) != 0 || result.Status != http.StatusNoContent || frameSeq != 0 {
		t.Fatalf("empty autofocus frame = %d bytes, seq=%d status=%d, error = %v", len(image), frameSeq, result.Status, err)
	}
	_, _, _, err = client.GetAutofocusFrameWithRequestID(t.Context(), "test-request")
	var requestError *RequestError
	if !errors.As(err, &requestError) || requestError.Class != "response_too_large" {
		t.Fatalf("oversized autofocus frame error = %v, want response_too_large", err)
	}
}

func TestGetAutofocusCalibrationWithRequestIDUsesAraContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/autofocus/calibration" {
			t.Errorf("Ara request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"samples":[{"focuser_position":1200,"star_count":87,"median_hfr":2.35,"median_fwhm":3.1,"median_roundness":0.8,"median_peak_to_background":4.2,"median_donut_outer_diameter":0,"median_donut_inner_diameter":0,"median_ring_thickness":0,"median_donut_shadow_depth":0}],"calibrated_utc":"2026-10-06T16:00:00Z","focuser_temperature_c":10.5,"filter":"Ha","curve_half_width_steps":50,"in_focus_hfr":2.1}`)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	calibration, _, err := client.GetAutofocusCalibrationWithRequestID(t.Context(), "test-request")
	if err != nil {
		t.Fatal(err)
	}
	if len(calibration.Samples) != 1 || calibration.Samples[0].FocuserPosition != 1200 || calibration.Filter == nil || *calibration.Filter != "Ha" || calibration.InFocusHFR == nil || *calibration.InFocusHFR != 2.1 {
		t.Fatalf("autofocus calibration = %+v", calibration)
	}
}

func TestAutofocusCancelAndRecalibrateUseAraRoutes(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Method+" "+r.URL.Path]++
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/autofocus/cancel":
			w.WriteHeader(http.StatusAccepted)
		case "POST /api/v1/autofocus/recalibrate":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("Ara request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := client.CancelAutofocusWithRequestID(t.Context(), "cancel-request")
	if err != nil || cancel.Outcome != OutcomeAccepted {
		t.Fatalf("autofocus cancellation = %+v, %v", cancel, err)
	}
	recalibrate, err := client.RecalibrateAutofocusWithRequestID(t.Context(), "recalibrate-request")
	if err != nil || recalibrate.Outcome != OutcomeCompleted {
		t.Fatalf("autofocus recalibration = %+v, %v", recalibrate, err)
	}
	if calls["POST /api/v1/autofocus/cancel"] != 1 || calls["POST /api/v1/autofocus/recalibrate"] != 1 {
		t.Fatalf("Ara calls = %v", calls)
	}
}
