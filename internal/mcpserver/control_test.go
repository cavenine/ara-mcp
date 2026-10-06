// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestControlManagerRetainsBoundedDeduplicatedAraEvents(t *testing.T) {
	manager, err := NewControlManager(nil, nil, "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.stop)
	manager.mu.Lock()
	manager.phase = "owned"
	manager.socketActive = true
	heartbeat := time.Now().UTC()
	manager.lastHeartbeat = &heartbeat
	manager.mu.Unlock()
	for seq := int64(1); seq <= maxRecentEvents+1; seq++ {
		manager.recordEvent(ara.WebSocketEvent{Type: "equipment.connected", Seq: seq, DeviceType: "camera", DeviceID: "camera-1", DeviceName: "ASI2600", State: "connected"})
	}
	manager.recordEvent(ara.WebSocketEvent{Type: "sequence.progress", Seq: maxRecentEvents, SequenceID: "seq-1"})
	snapshot := manager.RecentEvents()
	if !snapshot.Available || snapshot.Stale || !snapshot.Gap || snapshot.Dropped != 1 || snapshot.LastSequence != maxRecentEvents+1 || len(snapshot.Events) != maxRecentEvents {
		t.Fatalf("event snapshot = %+v", snapshot)
	}
	if snapshot.Events[0].Seq != 2 || snapshot.Events[len(snapshot.Events)-1].Seq != maxRecentEvents+1 {
		t.Fatalf("retained sequence range = %d..%d", snapshot.Events[0].Seq, snapshot.Events[len(snapshot.Events)-1].Seq)
	}
	if snapshot.Events[len(snapshot.Events)-1].DeviceType != "camera" || snapshot.Events[len(snapshot.Events)-1].DeviceID != "camera-1" || snapshot.Events[len(snapshot.Events)-1].DeviceName != "ASI2600" {
		t.Fatalf("retained equipment event = %+v", snapshot.Events[len(snapshot.Events)-1])
	}
}

func TestReadControlIdentityUsesActiveProfileListWhenServerStateOmitsID(t *testing.T) {
	const profileID = "e1d64755-e2ae-46f1-aa43-c6e67419e1e9"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":null,"ws_resume_token":"0"}`)
		case "/api/v1/profiles":
			_, _ = io.WriteString(w, `{"active_id":"`+profileID+`","profiles":[{"id":"`+profileID+`","name":"test"}]}`)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := readControlIdentity(t.Context(), client, "profile-request-01")
	if err != nil {
		t.Fatal(err)
	}
	if identity.profileID != profileID {
		t.Fatalf("active profile ID = %q, want %q", identity.profileID, profileID)
	}
}

func TestControlManager_BeginRequiresConfiguredProfile(t *testing.T) {
	var mutations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":null,"ws_resume_token":"0"}`)
		case "/api/v1/profiles":
			_, _ = io.WriteString(w, `{"active_id":null,"profiles":[]}`)
		default:
			mutations.Add(1)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)

	if _, err := control.Begin(t.Context(), "control-request-01"); err == nil || !strings.Contains(err.Error(), "configured") {
		t.Fatalf("Begin() error = %v, want configured-rig prerequisite", err)
	}
	if got := mutations.Load(); got != 0 {
		t.Fatalf("Ara mutation requests = %d, want no connect without an active profile", got)
	}
	if got := control.Snapshot().ControlOwnership; got != "denied" {
		t.Fatalf("control ownership = %q, want denied", got)
	}
}

func TestControlManager_RejectedClaimDoesNotBindSocket(t *testing.T) {
	var websocketRequests atomic.Int64
	var mutationDispatches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"ara-mcp","idle_seconds":1}`)
		case "/api/v1/server/connect":
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"title":"Connection rejected","detail":"Server in use by human"}`)
		case "/api/v1/ws":
			websocketRequests.Add(1)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	if _, err := control.Begin(t.Context(), "control-request-01"); err == nil {
		t.Fatal("Begin() error = nil, want existing Ara owner conflict")
	}
	if _, err := control.DispatchMutation(t.Context(), MutationRequest{
		ControlID: "", IntentID: "intent-01", Operation: "create_sequence", Arguments: []byte("command"), Kind: MutationNormal,
	}, func(context.Context) (MutationReceipt, error) {
		mutationDispatches.Add(1)
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}); !errors.Is(err, errControlID) {
		t.Fatalf("mutation without granted control error = %v, want control denial", err)
	}
	if got := websocketRequests.Load(); got != 0 {
		t.Fatalf("WebSocket requests = %d, want no bind after rejected claim", got)
	}
	if got := mutationDispatches.Load(); got != 0 {
		t.Fatalf("mutation dispatches after denied claim = %d, want 0", got)
	}
	if got := control.Snapshot().ControlOwnership; got != "denied" {
		t.Fatalf("control ownership = %q, want denied", got)
	}
}

func TestControlManager_UnknownClaimIsNotRetried(t *testing.T) {
	var connectCalls atomic.Int64
	var websocketCalls atomic.Int64
	var clientHostname atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			hostname, _ := clientHostname.Load().(string)
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"`+hostname+`","idle_seconds":1}`)
		case "/api/v1/server/connect":
			connectCalls.Add(1)
			var request struct {
				Hostname string `json:"hostname"`
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read connect request: %v", err)
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("decode connect request: %v", err)
			}
			clientHostname.Store(request.Hostname)
			<-r.Context().Done() // Ara granted the claim but the response was lost.
		case "/api/v1/ws":
			websocketCalls.Add(1)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	_, err = control.Begin(t.Context(), "control-request-01")
	if _, ok := errors.AsType[ControlClaimError](err); !ok {
		t.Fatalf("Begin() error = %v, want structured unknown claim result", err)
	}
	if got := control.Snapshot().ControlOwnership; got != "unknown" {
		t.Fatalf("control ownership after ambiguous claim = %q, want unknown", got)
	}
	_, err = control.Begin(t.Context(), "control-request-02")
	if _, ok := errors.AsType[ControlClaimError](err); !ok {
		t.Fatalf("second Begin() error = %v, want reconciliation-required unknown result", err)
	}
	if got := connectCalls.Load(); got != 1 {
		t.Fatalf("Ara connect requests = %d, want no blind retry", got)
	}
	if got := websocketCalls.Load(); got != 0 {
		t.Fatalf("WebSocket requests = %d, want no bind without the session capability", got)
	}
}

func TestControlManager_DispatchChecksAraIdentityBeforeMutation(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var restarted atomic.Bool
	var dispatches atomic.Int64
	var clientHostname atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverID := "server-01"
		if restarted.Load() {
			serverID = "server-02"
		}
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"`+serverID+`","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"`+serverID+`","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/connect":
			var request struct {
				Hostname string `json:"hostname"`
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read connect request: %v", err)
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("decode connect request: %v", err)
			}
			clientHostname.Store(request.Hostname)
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"`+request.Hostname+`","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/server/session":
			hostname, _ := clientHostname.Load().(string)
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"`+hostname+`","idle_seconds":1}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, _, err := conn.Read(context.Background()); err != nil {
				t.Errorf("read resume request: %v", err)
				return
			}
			if _, _, err := conn.Read(context.Background()); err == nil {
				t.Error("WebSocket read error = nil, want manager close")
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	started, err := control.Begin(t.Context(), "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	restarted.Store(true)
	request := MutationRequest{ControlID: started.ControlID, IntentID: "intent-after-restart", Operation: "create_sequence", Arguments: []byte("mutation"), Kind: MutationNormal}
	if _, err := control.DispatchMutation(t.Context(), request, func(context.Context) (MutationReceipt, error) {
		dispatches.Add(1)
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}); !errors.Is(err, errControlID) {
		t.Fatalf("DispatchMutation() after Ara restart error = %v, want fail-closed rejection", err)
	}
	if got := dispatches.Load(); got != 0 {
		t.Fatalf("mutation dispatches after Ara restart = %d, want 0", got)
	}
	if got := control.Snapshot().ControlOwnership; got != "connection_lost" {
		t.Fatalf("control ownership after restart detection = %q, want connection_lost", got)
	}
}

func TestControlManager_ReconnectsUsingTheSameAraSession(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var connectCalls atomic.Int64
	var websocketCalls atomic.Int64
	var clientHostname atomic.Value
	reconnected := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1","tier":"scaffold"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01"}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			hostname, _ := clientHostname.Load().(string)
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"`+hostname+`","idle_seconds":1}`)
		case "/api/v1/sequences":
			_, _ = io.WriteString(w, `{"items":[],"next_cursor":null,"has_more":false}`)
		case "/api/v1/server/connect":
			var request struct {
				Hostname  string  `json:"hostname"`
				SessionID *string `json:"session_id"`
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read connect body: %v", err)
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Errorf("decode connect body: %v", err)
			}
			clientHostname.Store(request.Hostname)
			call := connectCalls.Add(1)
			if call == 1 && request.SessionID != nil {
				t.Errorf("initial connect session_id = %v, want null", *request.SessionID)
			}
			if call > 1 && (request.SessionID == nil || *request.SessionID != sessionID) {
				t.Errorf("reconnect session_id = %v, want existing session", request.SessionID)
			}
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"`+request.Hostname+`","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if websocketCalls.Add(1) == 1 {
				if err := conn.Close(websocket.StatusGoingAway, "temporary disconnect"); err != nil {
					t.Errorf("close first WebSocket: %v", err)
				}
				return
			}
			ctx := context.Background()
			if _, payload, err := conn.Read(ctx); err != nil || string(payload) != `{"resume_token":"0"}` {
				t.Errorf("reconnect resume frame = %s, error=%v", payload, err)
				return
			}
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ping"}`)); err != nil {
				t.Errorf("send reconnect heartbeat: %v", err)
				return
			}
			if _, payload, err := conn.Read(ctx); err != nil || string(payload) != `{"type":"pong"}` {
				t.Errorf("reconnect heartbeat response = %s, error=%v", payload, err)
				return
			}
			close(reconnected)
			if _, _, err := conn.Read(ctx); err == nil {
				t.Error("reconnected socket read error = nil, want end_control close")
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown meter provider: %v", err)
		}
	})
	spanRecorder := tracetest.NewSpanRecorder()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spanRecorder))
	t.Cleanup(func() {
		if err := tracerProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown tracer provider: %v", err)
		}
	})
	control, err := NewControlManager(client, slog.New(slog.NewJSONHandler(&logs, nil)), "test", "stdio", meterProvider.Meter("reconnect-test"), tracerProvider.Tracer("control-test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.stop)
	control.reconnectInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started, err := control.Begin(ctx, "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconnected:
	case <-ctx.Done():
		t.Fatalf("same-session reconnect did not complete: %v", ctx.Err())
	}
	if err := control.Require(started.ControlID); err != nil {
		t.Fatalf("control ID after reconnect = %v, want still valid", err)
	}
	status := control.Snapshot()
	if status.LastHeartbeat == nil || status.LastReconciliation == nil {
		t.Fatalf("reconnect diagnostics = %+v, want heartbeat and reconciliation timestamps", status)
	}
	if got := collectCounter(t, reader, "ara.websocket.reconnects"); got != 1 {
		t.Fatalf("WebSocket reconnect attempts = %d, want 1", got)
	}
	if got := collectCounter(t, reader, "ara.websocket.heartbeats"); got != 1 {
		t.Fatalf("WebSocket heartbeats = %d, want 1", got)
	}
	if got := connectCalls.Load(); got != 2 {
		t.Fatalf("Ara connect calls = %d, want initial claim plus same-session reclaim", got)
	}
	if err := control.End(ctx, started.ControlID, "control-request-02"); err != nil {
		t.Fatal(err)
	}
	spans := make(map[string]bool)
	for _, span := range spanRecorder.Ended() {
		spans[span.Name()] = true
	}
	for _, name := range []string{"ara.control.begin", "ara.control.reconnect", "ara.control.end"} {
		if !spans[name] {
			t.Errorf("control trace omitted span %q", name)
		}
	}
	for _, event := range []string{"connection_lost", "connection_recovered", "control_released"} {
		if !bytes.Contains(logs.Bytes(), []byte(`"event":"`+event+`"`)) {
			t.Errorf("control lifecycle logs omitted %q", event)
		}
	}
	if bytes.Contains(logs.Bytes(), []byte(sessionID)) {
		t.Fatal("control lifecycle logs exposed Ara's session capability")
	}
}

func TestControlManager_InvalidatesControlWhenAraRestarts(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var infoCalls atomic.Int64
	var connectCalls atomic.Int64
	identityRead := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			call := infoCalls.Add(1)
			serverID := "server-01"
			if call > 1 {
				serverID = "server-02"
				select {
				case identityRead <- struct{}{}:
				default:
				}
			}
			_, _ = io.WriteString(w, `{"server_uuid":"`+serverID+`","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			serverID := "server-01"
			if infoCalls.Load() > 1 {
				serverID = "server-02"
			}
			_, _ = io.WriteString(w, `{"server_uuid":"`+serverID+`","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/connect":
			connectCalls.Add(1)
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"ara-mcp","idle_seconds":1}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, payload, err := conn.Read(context.Background()); err != nil || string(payload) != `{"resume_token":"0"}` {
				t.Errorf("initial resume frame = %s, error=%v", payload, err)
			}
			if err := conn.Close(websocket.StatusGoingAway, "Ara restarted"); err != nil {
				t.Errorf("close WebSocket for restart: %v", err)
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	control.reconnectInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started, err := control.Begin(ctx, "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-identityRead:
	case <-ctx.Done():
		t.Fatalf("Ara identity was not reread after socket loss: %v", ctx.Err())
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for control.Snapshot().ControlOwnership != "connection_lost" {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("control not invalidated after server UUID changed: %+v", control.Snapshot())
		}
	}
	if err := control.Require(started.ControlID); !errors.Is(err, errControlID) {
		t.Fatalf("Require(old control ID) = %v, want invalidated ID", err)
	}
	if got := connectCalls.Load(); got != 1 {
		t.Fatalf("Ara connect calls = %d, want no auto-claim after server restart", got)
	}
}

func TestControlManager_DoesNotReclaimExpiredSession(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var connectCalls atomic.Int64
	var sessionChecks atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			sessionChecks.Add(1)
			_, _ = io.WriteString(w, `{"connected":false,"hostname":null,"connected_at":null,"idle_seconds":null}`)
		case "/api/v1/server/connect":
			connectCalls.Add(1)
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, _, err := conn.Read(context.Background()); err != nil {
				t.Errorf("read resume frame: %v", err)
				return
			}
			if err := conn.Close(websocket.StatusGoingAway, "session expired"); err != nil {
				t.Errorf("close expired session socket: %v", err)
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	control.reconnectInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started, err := control.Begin(ctx, "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for control.Snapshot().ControlOwnership != "connection_lost" {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("expired session was not invalidated: %+v", control.Snapshot())
		}
	}
	if err := control.Require(started.ControlID); !errors.Is(err, errControlID) {
		t.Fatalf("Require(expired control ID) = %v, want invalidated ID", err)
	}
	if got := connectCalls.Load(); got != 1 {
		t.Fatalf("Ara connect calls = %d, want no auto-reclaim after session expiry", got)
	}
	if got := sessionChecks.Load(); got == 0 {
		t.Fatal("recovery did not inspect Ara's session state")
	}
}

func TestControlManager_BeginAndShutdownReleaseWithoutStoppingAraWork(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	var disconnects atomic.Int64
	takeoverHandled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/connect":
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"ara-mcp","idle_seconds":1}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, payload, err := conn.Read(context.Background()); err != nil || string(payload) != `{"resume_token":"0"}` {
				t.Errorf("resume frame = %s, error=%v", payload, err)
				return
			}
			if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"connection.request","request_id":"takeover-01","from":"human"}`)); err != nil {
				t.Errorf("send takeover request: %v", err)
				return
			}
			if _, payload, err := conn.Read(context.Background()); err != nil || string(payload) != `{"type":"connection.response","request_id":"takeover-01","action":"reject"}` {
				t.Errorf("takeover response = %s, error=%v", payload, err)
				return
			}
			close(takeoverHandled)
			if _, _, err := conn.Read(context.Background()); err == nil {
				t.Error("WebSocket read error = nil, want client close")
			}
		case "/api/v1/server/disconnect":
			disconnects.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := meterProvider.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown meter provider: %v", err)
		}
	})
	var logs bytes.Buffer
	control, err := NewControlManager(client, slog.New(slog.NewJSONHandler(&logs, nil)), "test", "stdio", meterProvider.Meter("control-test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.stop)
	defer control.Close(context.Background(), "")

	started, err := control.Begin(t.Context(), "control-request-01")
	if err != nil {
		t.Fatal(err)
	}
	if started.ControlID == "" || started.WebSocketState != "connected" {
		t.Fatalf("begin result = %+v, want a local control ID and bound socket", started)
	}
	select {
	case <-takeoverHandled:
	case <-time.After(time.Second):
		t.Fatal("session-bound socket did not reject the takeover request")
	}
	if got := collectCounter(t, reader, "ara.control.claims"); got != 1 {
		t.Fatalf("control claims = %d, want 1", got)
	}
	if got := collectCounter(t, reader, "ara.control.owned"); got != 1 {
		t.Fatalf("owned control sessions = %d, want 1", got)
	}
	if got := collectCounter(t, reader, "ara.websocket.active"); got != 1 {
		t.Fatalf("active control WebSockets = %d, want 1", got)
	}
	if err := control.Require(started.ControlID); err != nil {
		t.Fatalf("Require(current control ID) = %v, want nil", err)
	}
	if err := control.Require("stale-control-id"); err == nil {
		t.Fatal("Require(stale control ID) = nil, want rejection")
	}
	var dispatches atomic.Int64
	mutationRequest := MutationRequest{
		ControlID: started.ControlID, IntentID: "mutation-intent-01", Operation: "create_sequence", Arguments: []byte(`{"action":"test"}`), Kind: MutationNormal,
	}
	dispatch := func(context.Context) (MutationReceipt, error) {
		dispatches.Add(1)
		return MutationReceipt{Outcome: ara.OutcomeAccepted, ReceiptID: "receipt-01"}, nil
	}
	if _, err := control.DispatchMutation(t.Context(), mutationRequest, dispatch); err != nil {
		t.Fatal(err)
	}
	replay, err := control.DispatchMutation(t.Context(), mutationRequest, dispatch)
	if err != nil || !replay.Replayed || dispatches.Load() != 1 {
		t.Fatalf("mutation replay=%+v error=%v dispatches=%d", replay, err, dispatches.Load())
	}
	if got := collectCounter(t, reader, "ara.control.mutations"); got != 1 {
		t.Fatalf("mutation dispatch metric = %d, want 1", got)
	}
	if got := collectCounter(t, reader, "ara.control.mutation.replays"); got != 1 {
		t.Fatalf("mutation replay metric = %d, want 1", got)
	}
	if err := control.Close(context.Background(), "shutdown-control-request"); err != nil {
		t.Fatal(err)
	}
	if got := control.Snapshot().ControlOwnership; got != "not_owned" {
		t.Fatalf("control ownership after shutdown = %q, want not_owned", got)
	}
	if got := disconnects.Load(); got != 1 {
		t.Fatalf("Ara disconnect requests = %d, want 1", got)
	}
	if got := collectCounter(t, reader, "ara.control.owned"); got != 0 {
		t.Fatalf("owned control sessions after end = %d, want 0", got)
	}
	if got := collectCounter(t, reader, "ara.websocket.active"); got != 0 {
		t.Fatalf("active control WebSockets after end = %d, want 0", got)
	}
	for _, event := range []string{"control_acquired", "control_takeover_rejected", "control_released"} {
		if !bytes.Contains(logs.Bytes(), []byte(`"event":"`+event+`"`)) {
			t.Errorf("control logs omitted event %q", event)
		}
	}
	if bytes.Contains(logs.Bytes(), []byte(sessionID)) {
		t.Fatal("control logs exposed Ara's session capability")
	}
}

func TestControlToolsExposeBeginAndEnd(t *testing.T) {
	const sessionID = "b15e5138-12f0-4c41-8a43-ed79ef527e12"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/connect":
			_, _ = io.WriteString(w, `{"session_id":"`+sessionID+`","hostname":"ara-mcp","connected_at":"2026-10-05T00:00:00Z"}`)
		case "/api/v1/ws":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Errorf("accept WebSocket: %v", err)
				return
			}
			defer conn.CloseNow()
			if _, payload, err := conn.Read(context.Background()); err != nil || string(payload) != `{"resume_token":"0"}` {
				t.Errorf("resume frame = %s, error=%v", payload, err)
				return
			}
			if _, _, err := conn.Read(context.Background()); err == nil {
				t.Error("WebSocket read error = nil, want client close")
			}
		case "/api/v1/server/disconnect":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Ara request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := ara.New(ara.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	control := mustNewControlManager(t, client)
	defer control.Close(context.Background(), "")
	mcpServer, err := New(Options{Ara: client, Control: control})
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := mcpServer.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	tools, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, tool := range tools.Tools {
		seen[tool.Name] = true
	}
	if !seen["begin_control"] || !seen["end_control"] {
		t.Fatalf("control tools missing from discovery: %v", seen)
	}
	begin, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "begin_control"})
	if err != nil || begin.IsError {
		t.Fatalf("begin_control result=%+v error=%v", begin, err)
	}
	encoded, err := json.Marshal(begin.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var result BeginControlResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if result.ControlID == "" {
		t.Fatal("begin_control omitted control_id")
	}
	diagnosticsResult, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_adapter_diagnostics"})
	if err != nil || diagnosticsResult.IsError {
		t.Fatalf("get_adapter_diagnostics result=%+v error=%v", diagnosticsResult, err)
	}
	diagnosticsJSON, err := json.Marshal(diagnosticsResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics AdapterDiagnostics
	if err := json.Unmarshal(diagnosticsJSON, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.ControlOwnership != "owned" || diagnostics.WebSocketState != "connected" {
		t.Fatalf("diagnostics control=%q websocket=%q, want owned/connected", diagnostics.ControlOwnership, diagnostics.WebSocketState)
	}
	ended, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "end_control", Arguments: json.RawMessage(`{"control_id":"` + result.ControlID + `"}`),
	})
	if err != nil || ended.IsError {
		t.Fatalf("end_control result=%+v error=%v", ended, err)
	}
	diagnosticsResult, err = clientSession.CallTool(t.Context(), &mcp.CallToolParams{Name: "get_adapter_diagnostics"})
	if err != nil || diagnosticsResult.IsError {
		t.Fatalf("post-end diagnostics result=%+v error=%v", diagnosticsResult, err)
	}
	diagnosticsJSON, err = json.Marshal(diagnosticsResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(diagnosticsJSON, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if diagnostics.ControlOwnership != "not_owned" || diagnostics.WebSocketState != "not_connected" {
		t.Fatalf("post-end diagnostics control=%q websocket=%q, want not_owned/not_connected", diagnostics.ControlOwnership, diagnostics.WebSocketState)
	}
}

func TestControlManager_DispatchMutationReplaysIntentAndRejectsArgumentConflict(t *testing.T) {
	control := newMutationTestControl(t)
	var dispatches atomic.Int64
	request := MutationRequest{
		ControlID: "control-01", IntentID: "intent-01", Operation: "create_sequence", Arguments: []byte(`{"name":"M31"}`), Kind: MutationNormal,
	}
	dispatch := func(context.Context) (MutationReceipt, error) {
		dispatches.Add(1)
		return MutationReceipt{Outcome: ara.OutcomeAccepted, ReceiptID: "receipt-01"}, nil
	}
	first, err := control.DispatchMutation(t.Context(), request, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := control.DispatchMutation(t.Context(), request, dispatch)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.ReceiptID != first.ReceiptID || dispatches.Load() != 1 {
		t.Fatalf("first=%+v replay=%+v dispatches=%d, want one dispatch and same receipt", first, replayed, dispatches.Load())
	}
	request.Arguments = []byte(`{"name":"M42"}`)
	if _, err := control.DispatchMutation(t.Context(), request, dispatch); !errors.Is(err, errIntentConflict) {
		t.Fatalf("reused intent with different arguments error = %v, want intent conflict", err)
	}
	request.Arguments = []byte(`{"name":"M31"}`)
	request.Operation = "update_sequence"
	if _, err := control.DispatchMutation(t.Context(), request, dispatch); !errors.Is(err, errIntentConflict) {
		t.Fatalf("reused intent with different operation error = %v, want intent conflict", err)
	}
	uncertain := MutationRequest{ControlID: "control-01", IntentID: "intent-uncertain", Operation: "create_sequence", Arguments: []byte("uncertain"), Kind: MutationNormal}
	var uncertainDispatches atomic.Int64
	lostResponse := errors.New("lost response")
	uncertainDispatch := func(context.Context) (MutationReceipt, error) {
		uncertainDispatches.Add(1)
		return MutationReceipt{Outcome: ara.OutcomeUnknown, RetrySafe: true, ErrorClass: "timeout"}, lostResponse
	}
	unknown, err := control.DispatchMutation(t.Context(), uncertain, uncertainDispatch)
	var dispatchErr *MutationDispatchError
	if !errors.As(err, &dispatchErr) || !errors.Is(err, lostResponse) {
		t.Fatalf("unknown mutation error = %v, want bounded wrapper preserving error matching", err)
	}
	replayedUnknown, replayErr := control.DispatchMutation(t.Context(), uncertain, uncertainDispatch)
	var replay *MutationReplayError
	if !errors.As(replayErr, &replay) || replay.ErrorClass != "timeout" || !replayedUnknown.Replayed || !replayedUnknown.RetrySafe || unknown.Outcome != ara.OutcomeUnknown || uncertainDispatches.Load() != 1 {
		t.Fatalf("unknown=%+v replay=%+v error=%v dispatches=%d, want one retry-safe unknown receipt replay", unknown, replayedUnknown, replayErr, uncertainDispatches.Load())
	}
}

func TestControlManager_DispatchMutationSeparatesInterruptLaneAndGuardsRunState(t *testing.T) {
	control := newMutationTestControl(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	normalDone := make(chan error, 1)
	var guardedDispatches atomic.Int64
	go func() {
		_, err := control.DispatchMutation(t.Context(), MutationRequest{
			ControlID: "control-01", IntentID: "normal-01", Operation: "create_sequence", Arguments: []byte("normal"), Kind: MutationNormal,
		}, func(context.Context) (MutationReceipt, error) {
			close(entered)
			<-release
			return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
		})
		normalDone <- err
	}()
	<-entered
	if err := control.End(t.Context(), "control-01", "end-during-dispatch"); !errors.Is(err, errControlBusy) {
		t.Fatalf("End() during in-flight mutation error = %v, want busy", err)
	}
	if _, err := control.DispatchMutation(t.Context(), MutationRequest{
		ControlID: "control-01", IntentID: "normal-02", Operation: "update_sequence", Arguments: []byte("normal"), Kind: MutationNormal,
	}, func(context.Context) (MutationReceipt, error) {
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}); !errors.Is(err, errMutationBusy) {
		t.Fatalf("overlapping normal mutation error = %v, want busy", err)
	}
	if _, err := control.DispatchMutation(t.Context(), MutationRequest{
		ControlID: "control-01", IntentID: "interrupt-01", Operation: "emergency_stop", Arguments: []byte("interrupt"), Kind: MutationInterrupt,
	}, func(context.Context) (MutationReceipt, error) {
		return MutationReceipt{Outcome: ara.OutcomeCompleted}, nil
	}); err != nil {
		t.Fatalf("reserved interrupt lane error = %v, want nil", err)
	}
	close(release)
	if err := <-normalDone; err != nil {
		t.Fatal(err)
	}
	manual := MutationRequest{
		ControlID: "control-01", IntentID: "manual-01", Operation: "slew_telescope", Arguments: []byte("manual"), Kind: MutationManual,
		Preflight: func(context.Context) (RunSnapshot, error) {
			return RunSnapshot{Known: true, ActiveRun: true, RunID: "run-01"}, nil
		},
	}
	if _, err := control.DispatchMutation(t.Context(), manual, func(context.Context) (MutationReceipt, error) {
		guardedDispatches.Add(1)
		return MutationReceipt{}, nil
	}); !errors.Is(err, errRunActive) {
		t.Fatalf("manual action during active run error = %v, want active-run conflict", err)
	}
	stale := MutationRequest{
		ControlID: "control-01", IntentID: "pause-01", Operation: "pause_sequence", Arguments: []byte("pause"), Kind: MutationRunControl,
		ExpectedRunID: "run-old", Preflight: func(context.Context) (RunSnapshot, error) {
			return RunSnapshot{Known: true, ActiveRun: true, RunID: "run-new"}, nil
		},
	}
	if _, err := control.DispatchMutation(t.Context(), stale, func(context.Context) (MutationReceipt, error) {
		guardedDispatches.Add(1)
		return MutationReceipt{}, nil
	}); !errors.Is(err, errStaleRunID) {
		t.Fatalf("stale run control error = %v, want stale-run conflict", err)
	}
	unknown := MutationRequest{
		ControlID: "control-01", IntentID: "start-unknown-01", Operation: "start_sequence",
		Arguments: []byte("start"), Kind: MutationStartRun,
		Preflight: func(context.Context) (RunSnapshot, error) { return RunSnapshot{}, nil },
	}
	if _, err := control.DispatchMutation(t.Context(), unknown, func(context.Context) (MutationReceipt, error) {
		guardedDispatches.Add(1)
		return MutationReceipt{}, nil
	}); !errors.Is(err, errRunStateUnknown) {
		t.Fatalf("start with incomplete run observation error = %v, want fail-closed unknown", err)
	}
	if got := guardedDispatches.Load(); got != 0 {
		t.Fatalf("guarded dispatcher calls = %d, want 0", got)
	}
}

func TestControlManager_RunAbortUsesReservedLaneAndExpectedRunID(t *testing.T) {
	control := newMutationTestControl(t)
	control.normal <- struct{}{}
	dispatches := 0
	request := MutationRequest{
		ControlID: "control-01", IntentID: "abort-run-01", Operation: "abort_sequence",
		Arguments: []byte("abort"), Kind: MutationInterrupt, ExpectedRunID: "run-01",
		Preflight: func(context.Context) (RunSnapshot, error) {
			return RunSnapshot{Known: true, ActiveRun: true, RunID: "run-01"}, nil
		},
	}
	if _, err := control.DispatchMutation(t.Context(), request, func(context.Context) (MutationReceipt, error) {
		dispatches++
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}); err != nil {
		t.Fatalf("reserved run abort error = %v, want dispatch despite saturated normal lane", err)
	}
	if dispatches != 1 || len(control.normal) != 1 {
		t.Fatalf("dispatches = %d, normal lane occupancy = %d; want one interrupt dispatch and normal lane unchanged", dispatches, len(control.normal))
	}
	request.IntentID = "abort-run-stale"
	request.ExpectedRunID = "run-old"
	if _, err := control.DispatchMutation(t.Context(), request, func(context.Context) (MutationReceipt, error) {
		dispatches++
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}); !errors.Is(err, errStaleRunID) {
		t.Fatalf("stale run abort error = %v, want stale run ID error", err)
	}
	if dispatches != 1 {
		t.Fatalf("stale run abort dispatched; total dispatches = %d", dispatches)
	}
}

func TestControlManager_DispatchMutationRejectsNewIntentsAtCapacity(t *testing.T) {
	control := newMutationTestControl(t)
	dispatch := func(context.Context) (MutationReceipt, error) {
		return MutationReceipt{Outcome: ara.OutcomeAccepted}, nil
	}
	for i := range maxMutationIntents {
		request := MutationRequest{
			ControlID: "control-01", IntentID: fmt.Sprintf("intent-%04d", i), Operation: "create_sequence",
			Arguments: []byte("bounded"), Kind: MutationNormal,
		}
		if _, err := control.DispatchMutation(t.Context(), request, dispatch); err != nil {
			t.Fatalf("DispatchMutation(%d) error = %v before intent capacity", i, err)
		}
	}
	request := MutationRequest{ControlID: "control-01", IntentID: "intent-overflow", Operation: "create_sequence", Arguments: []byte("bounded"), Kind: MutationNormal}
	if _, err := control.DispatchMutation(t.Context(), request, dispatch); !errors.Is(err, errIntentCapacity) {
		t.Fatalf("DispatchMutation() error = %v, want bounded intent capacity error", err)
	}
	interrupt := MutationRequest{ControlID: "control-01", IntentID: "stop-overflow", Operation: "emergency_stop", Arguments: []byte("stop"), Kind: MutationInterrupt}
	if _, err := control.DispatchMutation(t.Context(), interrupt, dispatch); err != nil {
		t.Fatalf("reserved interrupt at receipt capacity error = %v, want dispatch", err)
	}
}

func newMutationTestControl(t *testing.T) *ControlManager {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/info":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","version":"1.0.0","api":"v1"}`)
		case "/api/v1/server/versions":
			_, _ = io.WriteString(w, `{"daemon_version":"1.0.0","daemon_git_sha":"build-01","api_surfaces":[{"name":"rest","version":"1.0.0"}]}`)
		case "/api/v1/server/state":
			_, _ = io.WriteString(w, `{"server_uuid":"server-01","current_profile_id":"profile-01","ws_resume_token":"0"}`)
		case "/api/v1/server/session":
			_, _ = io.WriteString(w, `{"connected":true,"hostname":"mutation-test","idle_seconds":1}`)
		default:
			t.Errorf("unexpected mutation preflight request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	client, err := ara.New(ara.Config{BaseURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewControlManager(client, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.stop)
	control.mu.Lock()
	control.phase = "owned"
	control.controlID = "control-01"
	control.identity = controlIdentity{
		serverUUID: "server-01", serverVersion: "1.0.0", apiVersion: "v1",
		daemonVersion: "1.0.0", daemonGitSHA: "build-01", apiSurfaces: []ara.APISurface{{Name: "rest", Version: "1.0.0"}},
		profileID: "profile-01", resumeToken: "0",
	}
	control.session = ara.ControlSession{Hostname: "mutation-test"}
	control.mu.Unlock()
	return control
}

func mustNewControlManager(t *testing.T, client *ara.Client) *ControlManager {
	t.Helper()
	control, err := NewControlManager(client, nil, "test", "stdio", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(control.stop)
	return control
}

func collectCounter(t *testing.T, reader *sdkmetric.ManualReader, name string) int64 {
	t.Helper()
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, recorded := range scope.Metrics {
			if recorded.Name != name {
				continue
			}
			data, ok := recorded.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("%s data = %T, want integer sum", name, recorded.Data)
			}
			var total int64
			for _, point := range data.DataPoints {
				total += point.Value
			}
			return total
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}
