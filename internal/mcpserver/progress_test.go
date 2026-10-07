// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFramePreviewReturnsMCPImageContent(t *testing.T) {
	preview := make([]byte, 1<<20)
	preview[0], preview[1], preview[len(preview)-2], preview[len(preview)-1] = 0xff, 0xd8, 0xff, 0xd9
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/frames/frame-01/thumbnail":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(preview)
		case "/api/v1/jobs/job-01":
			_, _ = w.Write([]byte(`{"job_id":"job-01","job_type":"autofocus","state":"running","done":3,"total":9,"started_utc":"2026-10-05T00:00:00Z"}`))
		case "/api/v1/frames":
			_, _ = w.Write([]byte(`{"items":[{"id":"frame-01","target_name":"M31"}],"next_cursor":null,"has_more":false}`))
		case "/api/v1/frames/frame-01":
			_, _ = w.Write([]byte(`{"id":"frame-01","target_name":"M31","width":800,"height":600}`))
		default:
			t.Errorf("unexpected route = %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControlManager(nil, nil, "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	control.mu.Lock()
	control.phase = "owned"
	control.socketActive = true
	heartbeat := time.Now().UTC()
	control.lastHeartbeat = &heartbeat
	control.mu.Unlock()
	control.recordEvent(ara.WebSocketEvent{Type: "sequence.progress", Seq: 1, SequenceID: "seq-1", RunID: "run-1", State: "running", InstructionsCompleted: 2, InstructionsTotal: 5})
	exposureSeconds, elapsedMS := 30.0, int64(30120)
	control.recordEvent(ara.WebSocketEvent{Type: "camera.exposure_complete", Seq: 2, Exposure: &ara.ExposureEventContext{FrameID: "frame-02", ExposureSec: &exposureSeconds, ElapsedMS: &elapsedMS}})
	t.Cleanup(control.stop)
	server, err := New(Options{Ara: client, Control: control})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	jobResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_job_status", Arguments: map[string]any{"job_id": "job-01"}})
	if err != nil || jobResult.IsError {
		t.Fatalf("job status result = %+v, error = %v", jobResult, err)
	}
	jobBody, err := json.Marshal(jobResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var job ara.Job
	if err := json.Unmarshal(jobBody, &job); err != nil || job.State != "running" || job.Done != 3 || job.Total != 9 {
		t.Fatalf("job = %+v, error = %v", job, err)
	}
	framePage, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "list_frames", Arguments: map[string]any{"limit": 5}})
	if err != nil || framePage.IsError {
		t.Fatalf("frame page = %+v, error = %v", framePage, err)
	}
	frame, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_frame", Arguments: map[string]any{"frame_id": "frame-01"}})
	if err != nil || frame.IsError {
		t.Fatalf("frame detail = %+v, error = %v", frame, err)
	}
	result, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_frame_preview", Arguments: map[string]any{"frame_id": "frame-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("result = %+v", result)
	}
	image, ok := result.Content[0].(*mcp.ImageContent)
	if !ok || image.MIMEType != "image/jpeg" || len(image.Data) != len(preview) || image.Data[0] != 0xff || image.Data[1] != 0xd8 || image.Data[len(image.Data)-2] != 0xff || image.Data[len(image.Data)-1] != 0xd9 {
		t.Fatalf("image content = %#v", result.Content[0])
	}
	events, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_recent_ara_events"})
	if err != nil || events.IsError {
		t.Fatalf("event result = %+v, error = %v", events, err)
	}
	eventBody, err := json.Marshal(events.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot EventSnapshot
	if err := json.Unmarshal(eventBody, &snapshot); err != nil || !snapshot.Available || snapshot.Stale || len(snapshot.Events) != 2 || snapshot.Events[0].RunID != "run-1" || snapshot.Events[0].InstructionsCompleted != 2 || snapshot.Events[0].InstructionsTotal != 5 || snapshot.Events[1].Exposure == nil || snapshot.Events[1].Exposure.FrameID != "frame-02" || snapshot.Events[1].Exposure.ElapsedMS == nil || *snapshot.Events[1].Exposure.ElapsedMS != elapsedMS {
		t.Fatalf("event snapshot = %+v, error = %v", snapshot, err)
	}
}
