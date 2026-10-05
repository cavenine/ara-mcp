// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/coder/websocket"
)

var (
	errControlBusy = errors.New("Ara control phase is already active or changing")
	errControlID   = errors.New("control_id is invalid or control is unavailable")
)

// ControlStatus exposes ownership and socket health without the Ara session
// capability or local control ID.
type ControlStatus struct {
	ControlOwnership string     `json:"control_ownership"`
	WebSocketState   string     `json:"websocket_state"`
	LastHeartbeat    *time.Time `json:"last_heartbeat"`
}

// BeginControlResult contains the local capability returned by begin_control.
type BeginControlResult struct {
	ControlID      string    `json:"control_id"`
	WebSocketState string    `json:"websocket_state"`
	ConnectedAt    time.Time `json:"connected_at"`
}

// ControlManager owns the single adapter control phase for this process.
type ControlManager struct {
	mu             sync.Mutex
	client         *ara.Client
	logger         *slog.Logger
	version        string
	transport      string
	phase          string
	controlID      string
	requestID      string
	session        ara.ControlSession
	connection     *websocket.Conn
	connectionStop context.CancelFunc
	connectionDone chan struct{}
	lastHeartbeat  *time.Time
	root           context.Context
	stop           context.CancelFunc
	closed         bool
}

// NewControlManager creates the process-local cooperative controller.
func NewControlManager(client *ara.Client, logger *slog.Logger, version, transport string) *ControlManager {
	if logger == nil {
		logger = slog.Default()
	}
	if version == "" {
		version = "dev"
	}
	if transport == "" {
		transport = "stdio"
	}
	root, stop := context.WithCancel(context.Background())
	return &ControlManager{client: client, logger: logger, version: version, transport: transport, phase: "not_owned", root: root, stop: stop}
}

// Begin checks for an active Ara profile, claims the control slot, and binds its
// session WebSocket before returning a local control ID.
func (m *ControlManager) Begin(ctx context.Context, requestID string) (BeginControlResult, error) {
	if ctx == nil || m.client == nil {
		return BeginControlResult{}, errors.New("Ara control manager is not configured")
	}
	m.mu.Lock()
	if m.closed || m.phase != "not_owned" {
		m.mu.Unlock()
		return BeginControlResult{}, errControlBusy
	}
	m.phase = "acquiring"
	m.mu.Unlock()

	var state struct {
		CurrentProfileID *string `json:"current_profile_id"`
	}
	if _, err := m.client.Do(ctx, ara.Request{Method: http.MethodGet, Route: "/server/state", RequestID: requestID}, &state); err != nil {
		m.resetAcquire()
		return BeginControlResult{}, fmt.Errorf("check configured Ara profile: %w", err)
	}
	if state.CurrentProfileID == nil || strings.TrimSpace(*state.CurrentProfileID) == "" {
		m.resetAcquire()
		return BeginControlResult{}, errors.New("begin control requires a configured active Ara profile")
	}

	session, _, err := m.client.ConnectWithRequestID(ctx, "ara-mcp", nil, requestID)
	if err != nil {
		m.resetAcquire()
		return BeginControlResult{}, fmt.Errorf("claim Ara control session: %w", err)
	}
	connection, err := m.client.OpenControlWebSocket(ctx, session)
	if err != nil {
		return BeginControlResult{}, errors.Join(fmt.Errorf("bind Ara control WebSocket: %w", err), m.releaseAfterFailedBind(ctx, session, requestID))
	}
	controlID, err := newControlID()
	if err != nil {
		_ = connection.CloseNow()
		return BeginControlResult{}, errors.Join(fmt.Errorf("create local control ID: %w", err), m.releaseAfterFailedBind(ctx, session, requestID))
	}

	connectionContext, connectionStop := context.WithCancel(m.root)
	connectionDone := make(chan struct{})
	m.mu.Lock()
	if m.closed {
		m.phase = "not_owned"
		m.mu.Unlock()
		connectionStop()
		_ = connection.CloseNow()
		return BeginControlResult{}, errors.Join(errControlBusy, m.releaseAfterFailedBind(ctx, session, requestID))
	}
	m.phase = "owned"
	m.controlID = controlID
	m.requestID = requestID
	m.session = session
	m.connection = connection
	m.connectionStop = connectionStop
	m.connectionDone = connectionDone
	m.lastHeartbeat = nil
	m.mu.Unlock()

	go m.maintainConnection(connectionContext, connection, connectionDone)
	m.logger.InfoContext(ctx, "Ara control acquired", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_acquired", "transport", m.transport, "request_id", requestID, "websocket_state", "connected")
	return BeginControlResult{ControlID: controlID, WebSocketState: "connected", ConnectedAt: session.ConnectedAt}, nil
}

// End invalidates the local capability before releasing Ara's session. It never
// stops or aborts Ara work.
func (m *ControlManager) End(ctx context.Context, controlID, requestID string) error {
	if ctx == nil {
		return errors.New("control end context is required")
	}
	m.mu.Lock()
	if controlID == "" || controlID != m.controlID || (m.phase != "owned" && m.phase != "connection_lost") {
		m.mu.Unlock()
		return errControlID
	}
	m.phase = "ending"
	session, connection := m.session, m.connection
	connectionStop, connectionDone := m.connectionStop, m.connectionDone
	m.controlID = ""
	m.requestID = ""
	m.mu.Unlock()

	if connectionStop != nil {
		connectionStop()
	}
	var closeErr error
	if connection != nil {
		closeErr = connection.CloseNow()
	}
	var waitErr error
	if connectionDone != nil {
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-connectionDone:
		case <-timer.C:
			waitErr = errors.New("Ara control WebSocket reader did not stop")
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
	disconnectContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_, disconnectErr := m.client.DisconnectWithRequestID(disconnectContext, session, requestID)
	cancel()
	if apiError, ok := errors.AsType[*ara.APIError](disconnectErr); ok && apiError.Status == http.StatusNotFound {
		disconnectErr = nil
	}
	m.mu.Lock()
	m.phase = "not_owned"
	m.session = ara.ControlSession{}
	m.connection = nil
	m.connectionStop = nil
	m.connectionDone = nil
	m.lastHeartbeat = nil
	m.mu.Unlock()
	m.logger.InfoContext(ctx, "Ara control released", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_released", "transport", m.transport, "request_id", requestID)
	return errors.Join(closeErr, waitErr, disconnectErr)
}

// Close releases a current session best-effort and stops manager-owned work.
func (m *ControlManager) Close(ctx context.Context, requestID string) error {
	m.mu.Lock()
	if m.phase == "acquiring" || m.phase == "ending" {
		m.mu.Unlock()
		return errControlBusy
	}
	controlID := m.controlID
	m.closed = true
	m.mu.Unlock()
	var err error
	if controlID != "" {
		err = m.End(ctx, controlID, requestID)
	}
	m.stop()
	return err
}

// Snapshot returns bounded public state; it never includes either capability.
func (m *ControlManager) Snapshot() ControlStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	ownership := "not_owned"
	websocketState := "not_connected"
	switch m.phase {
	case "acquiring":
		ownership = "acquiring"
	case "owned":
		ownership, websocketState = "owned", "connected"
	case "connection_lost":
		ownership, websocketState = "connection_lost", "disconnected"
	case "ending":
		ownership, websocketState = "releasing", "closing"
	}
	status := ControlStatus{ControlOwnership: ownership, WebSocketState: websocketState}
	if m.lastHeartbeat != nil {
		last := *m.lastHeartbeat
		status.LastHeartbeat = &last
	}
	return status
}

// Require rejects mutations unless the caller presents the live phase ID.
func (m *ControlManager) Require(controlID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.phase != "owned" || controlID == "" || controlID != m.controlID {
		return errControlID
	}
	return nil
}

func (m *ControlManager) maintainConnection(ctx context.Context, connection *websocket.Conn, done chan<- struct{}) {
	defer close(done)
	_ = m.client.MaintainControlWebSocket(ctx, connection, func(at time.Time) {
		m.mu.Lock()
		if m.connection == connection && m.phase == "owned" {
			last := at
			m.lastHeartbeat = &last
		}
		m.mu.Unlock()
	}, func() {
		m.logger.WarnContext(ctx, "Ara control takeover rejected", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_takeover_rejected", "transport", m.transport, "request_id", m.controlRequestID(connection))
	})
	if ctx.Err() != nil {
		return
	}
	m.mu.Lock()
	if m.connection == connection && m.phase == "owned" {
		m.phase = "connection_lost"
	}
	m.mu.Unlock()
	m.logger.Warn("Ara control WebSocket lost", "service", "ara-mcp", "version", m.version, "component", "control", "event", "connection_lost", "transport", m.transport, "request_id", m.controlRequestID(connection), "error_class", "websocket")
}

func (m *ControlManager) controlRequestID(connection *websocket.Conn) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.connection == connection {
		return m.requestID
	}
	return ""
}

func (m *ControlManager) resetAcquire() {
	m.mu.Lock()
	m.phase = "not_owned"
	m.mu.Unlock()
}

func (m *ControlManager) releaseAfterFailedBind(ctx context.Context, session ara.ControlSession, requestID string) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := m.client.DisconnectWithRequestID(cleanupContext, session, requestID)
	m.resetAcquire()
	return err
}

func newControlID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
