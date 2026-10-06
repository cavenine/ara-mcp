// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cavenine/ara-mcp/internal/ara"
	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"uuid"
)

var (
	errControlBusy     = errors.New("ara control phase is already active or changing")
	errControlID       = errors.New("control_id is invalid or control is unavailable")
	errIntentConflict  = errors.New("intent_id was already used with different arguments")
	errIntentCapacity  = errors.New("control phase intent receipt capacity exhausted")
	errMutationBusy    = errors.New("another mutation is already being dispatched")
	errRunActive       = errors.New("manual action or sequence start conflicts with an active run")
	errRunInactive     = errors.New("ara sequence run is not active")
	errStaleRunID      = errors.New("expected_run_id does not match current Ara state")
	errRunStateUnknown = errors.New("ara run state is unavailable; mutation was not dispatched")
	errClaimUnknown    = errors.New("ara control claim is unresolved; inspect the Ara session before retrying")
	errInvalidIntent   = errors.New("mutation intent is invalid")
)

// ControlClaimError marks an ambiguous Ara claim; callers must reconcile the
// session rather than blindly issue another fresh claim.
type ControlClaimError struct{}

func (ControlClaimError) Error() string {
	return "ara control claim outcome is unknown; reconcile the session before beginning again"
}

func (ControlClaimError) Is(target error) bool { return target == errClaimUnknown }

const (
	maxMutationIntents       = 1024
	maxMutationIntentIDBytes = 128
	maxMutationArgumentBytes = 2 << 20
	maxMutationLedgerBytes   = 1 << 20
	mutationReceiptBudget    = 512 // Includes receipt metadata, entry, and channel overhead.
	maxIntentWaiters         = 4
	maxRecentEvents          = 128
	maxRecentEventBytes      = 1 << 20
	recentEventMetadataBytes = 4096 // Conservative per-event bound including device, fault, and timestamp details.
)

// MutationKind selects the adapter admission policy for a mutating request.
type MutationKind string

const (
	MutationNormal     MutationKind = "normal"
	MutationManual     MutationKind = "manual"
	MutationStartRun   MutationKind = "start_run"
	MutationRunControl MutationKind = "run_control"
	MutationInterrupt  MutationKind = "interrupt"
)

// MutationRequest carries the local authorization, operation identity, and a
// bounded canonical argument encoding. Run state is refreshed before dispatch.
type MutationRequest struct {
	ControlID     string
	IntentID      string
	Operation     string
	Arguments     []byte
	Kind          MutationKind
	ExpectedRunID string
	Preflight     func(context.Context) (RunSnapshot, error)
}

// RunSnapshot is a fresh Ara observation obtained inside the normal mutation
// admission lane before the command is dispatched. Known must be false when the
// observed scope cannot establish the condition required for that action; never
// treat Ara's limited sequence list as a global active-run scan.
type RunSnapshot struct {
	Known     bool
	ActiveRun bool
	RunID     string
}

// MutationReceipt stores only outcome metadata, never request or result bodies.
type MutationReceipt struct {
	Outcome    ara.Outcome `json:"outcome"`
	ReceiptID  string      `json:"receipt_id,omitempty"`
	SequenceID string      `json:"sequence_id,omitempty"`
	ErrorClass string      `json:"error_class,omitempty"`
	RetrySafe  bool        `json:"retry_safe"`
	Replayed   bool        `json:"replayed,omitempty"`
}

type mutationIntent struct {
	arguments [sha256.Size]byte
	done      chan struct{}
	receipt   MutationReceipt
	waiters   int
}

type controlIdentity struct {
	serverUUID    string
	serverVersion string
	apiVersion    string
	daemonVersion string
	daemonGitSHA  string
	apiSurfaces   []ara.APISurface
	profileID     string
	resumeToken   string
}

func (i controlIdentity) sameDaemon(other controlIdentity) bool {
	return i.serverUUID != "" && i.serverUUID == other.serverUUID &&
		i.serverVersion == other.serverVersion && i.apiVersion == other.apiVersion &&
		i.daemonVersion == other.daemonVersion && i.daemonGitSHA == other.daemonGitSHA &&
		slices.Equal(i.apiSurfaces, other.apiSurfaces)
}

type controlMetrics struct {
	claims     metric.Int64Counter
	owned      metric.Int64UpDownCounter
	websockets metric.Int64UpDownCounter
	reconnects metric.Int64Counter
	heartbeats metric.Int64Counter
	mutations  metric.Int64Counter
	replays    metric.Int64Counter
	events     metric.Int64Counter
}

// ControlStatus exposes ownership and socket health without the Ara session
// capability or local control ID.
type ControlStatus struct {
	ControlOwnership   string     `json:"control_ownership"`
	WebSocketState     string     `json:"websocket_state"`
	LastHeartbeat      *time.Time `json:"last_heartbeat"`
	LastReconciliation *time.Time `json:"last_reconciliation"`
	LastEvent          *time.Time `json:"last_event"`
	LastEventSequence  int64      `json:"last_event_sequence"`
	EventGap           bool       `json:"event_gap"`
	EventBacklog       int        `json:"event_backlog"`
	DroppedEvents      int64      `json:"dropped_events"`
}

// EventSnapshot is an immutable bounded view of the current control event buffer.
type EventSnapshot struct {
	Available    bool                 `json:"available"`
	Stale        bool                 `json:"stale"`
	Gap          bool                 `json:"gap"`
	LastSequence int64                `json:"last_sequence"`
	Dropped      int64                `json:"dropped"`
	Events       []ara.WebSocketEvent `json:"events"`
}

// BeginControlResult contains the local capability returned by begin_control.
type BeginControlResult struct {
	ControlID      string    `json:"control_id"`
	WebSocketState string    `json:"websocket_state"`
	ConnectedAt    time.Time `json:"connected_at"`
}

// ControlManager owns the single adapter control phase for this process.
type ControlManager struct {
	mu                   sync.Mutex
	client               *ara.Client
	logger               *slog.Logger
	tracer               trace.Tracer
	beginTrace           trace.SpanContext
	version              string
	transport            string
	phase                string
	controlID            string
	requestID            string
	session              ara.ControlSession
	connection           *websocket.Conn
	connectionStop       context.CancelFunc
	connectionDone       chan struct{}
	lastHeartbeat        *time.Time
	lastReconciliation   *time.Time
	identity             controlIdentity
	clientHostname       string
	reconnectInterval    time.Duration
	reconnectMaxInterval time.Duration
	reconnectWindow      time.Duration
	socketActive         bool
	normal               chan struct{}
	interrupt            chan struct{}
	intents              map[string]*mutationIntent
	ledgerBytes          int
	inFlight             int
	root                 context.Context
	stop                 context.CancelFunc
	closed               bool
	changed              chan struct{}
	metrics              controlMetrics
	events               []ara.WebSocketEvent
	eventBytes           int
	lastEventAt          *time.Time
	lastEventSequence    int64
	droppedEvents        int64
	eventGap             bool
}

// NewControlManager creates the process-local cooperative controller.
func NewControlManager(client *ara.Client, logger *slog.Logger, version, transport string, meter metric.Meter, tracer trace.Tracer) (*ControlManager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if meter == nil {
		meter = metricnoop.NewMeterProvider().Meter("github.com/cavenine/ara-mcp/internal/mcpserver/control")
	}
	if tracer == nil {
		tracer = tracenoop.NewTracerProvider().Tracer("github.com/cavenine/ara-mcp/internal/mcpserver/control")
	}
	metrics, err := newControlMetrics(meter)
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = "dev"
	}
	if transport == "" {
		transport = "stdio"
	}
	root, stop := context.WithCancel(context.Background())
	return &ControlManager{
		client: client, logger: logger, tracer: tracer, version: version, transport: transport, phase: "not_owned",
		normal: make(chan struct{}, 1), interrupt: make(chan struct{}, 1), intents: make(map[string]*mutationIntent),
		clientHostname: "ara-mcp-" + uuid.NewV4().String()[:8], reconnectInterval: 500 * time.Millisecond,
		reconnectMaxInterval: 5 * time.Second, reconnectWindow: 50 * time.Second,
		root: root, stop: stop, changed: make(chan struct{}), metrics: metrics,
	}, nil
}

func newControlMetrics(meter metric.Meter) (controlMetrics, error) {
	var metrics controlMetrics
	var err error
	if metrics.claims, err = meter.Int64Counter("ara.control.claims", metric.WithUnit("{claim}"), metric.WithDescription("Ara control claims by bounded outcome")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara control claim counter: %w", err)
	}
	if metrics.owned, err = meter.Int64UpDownCounter("ara.control.owned", metric.WithUnit("{session}"), metric.WithDescription("Ara control sessions held by the adapter")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara control ownership counter: %w", err)
	}
	if metrics.websockets, err = meter.Int64UpDownCounter("ara.websocket.active", metric.WithUnit("{connection}"), metric.WithDescription("Session-bound Ara WebSocket connections currently active")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara WebSocket active counter: %w", err)
	}
	if metrics.reconnects, err = meter.Int64Counter("ara.websocket.reconnects", metric.WithUnit("{attempt}"), metric.WithDescription("Session-bound Ara WebSocket recovery attempts")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara WebSocket reconnect counter: %w", err)
	}
	if metrics.heartbeats, err = meter.Int64Counter("ara.websocket.heartbeats", metric.WithUnit("{heartbeat}"), metric.WithDescription("Ara application-level heartbeats received on the bound session socket")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara WebSocket heartbeat counter: %w", err)
	}
	if metrics.mutations, err = meter.Int64Counter("ara.control.mutations", metric.WithUnit("{mutation}"), metric.WithDescription("Adapter control mutation dispatch outcomes")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara control mutation counter: %w", err)
	}
	if metrics.replays, err = meter.Int64Counter("ara.control.mutation.replays", metric.WithUnit("{replay}"), metric.WithDescription("Mutation intents replayed without another Ara dispatch")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara mutation replay counter: %w", err)
	}
	if metrics.events, err = meter.Int64Counter("ara.events.processed", metric.WithUnit("{event}"), metric.WithDescription("Ara WebSocket events processed by bounded category")); err != nil {
		return controlMetrics{}, fmt.Errorf("create Ara event counter: %w", err)
	}
	return metrics, nil
}

func readControlIdentity(ctx context.Context, client *ara.Client, requestID string) (controlIdentity, error) {
	info, _, err := client.GetServerInfoWithRequestID(ctx, requestID)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("read Ara server info before control: %w", err)
	}
	versions, _, err := client.GetServerVersionsWithRequestID(ctx, requestID)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("read Ara server versions before control: %w", err)
	}
	stateBody, _, err := client.GetServerStateWithRequestID(ctx, requestID)
	if err != nil {
		return controlIdentity{}, fmt.Errorf("read Ara server state before control: %w", err)
	}
	var state struct {
		ServerUUID       string  `json:"server_uuid"`
		CurrentProfileID *string `json:"current_profile_id"`
		ResumeToken      string  `json:"ws_resume_token"`
	}
	if err := json.Unmarshal(stateBody, &state); err != nil {
		return controlIdentity{}, fmt.Errorf("decode Ara server state before control: %w", err)
	}
	identity := controlIdentity{
		serverUUID: info.UUID, serverVersion: info.Version, apiVersion: info.API,
		daemonVersion: versions.DaemonVersion, daemonGitSHA: versions.DaemonGitSHA,
		apiSurfaces: slices.Clone(versions.APISurfaces), resumeToken: state.ResumeToken,
	}
	if identity.serverUUID == "" || identity.serverVersion == "" || identity.apiVersion == "" || state.ServerUUID != identity.serverUUID {
		return controlIdentity{}, errors.New("Ara server identity is incomplete or inconsistent")
	}
	identity.profileID, err = selectedProfileID(ctx, client, state.CurrentProfileID, requestID)
	if err != nil {
		return controlIdentity{}, err
	}
	return identity, nil
}

func selectedProfileID(ctx context.Context, client *ara.Client, serverStateID *string, requestID string) (string, error) {
	if serverStateID != nil && strings.TrimSpace(*serverStateID) != "" {
		return strings.TrimSpace(*serverStateID), nil
	}
	profiles, _, err := client.ListProfilesWithRequestID(ctx, requestID)
	if err != nil {
		return "", fmt.Errorf("read selected Ara profile: %w", err)
	}
	if profiles.ActiveID == nil {
		return "", nil
	}
	return strings.TrimSpace(*profiles.ActiveID), nil
}

// Begin checks for an active Ara profile, claims the control slot, and binds its
// session WebSocket before returning a local control ID.
func (m *ControlManager) Begin(ctx context.Context, requestID string) (result BeginControlResult, retErr error) {
	if ctx == nil || m.client == nil {
		return BeginControlResult{}, errors.New("ara control manager is not configured")
	}
	ctx, span := m.tracer.Start(ctx, "ara.control.begin")
	defer span.End()
	defer func() {
		if retErr != nil {
			span.RecordError(errors.New("control begin failed"))
			span.SetStatus(codes.Error, "control begin failed")
		}
	}()
	claimOutcome := "error"
	defer func() {
		m.metrics.claims.Add(context.WithoutCancel(ctx), 1, metric.WithAttributes(attribute.String("outcome", claimOutcome)))
	}()
	m.mu.Lock()
	previousPhase := m.phase
	if m.closed || (previousPhase != "not_owned" && previousPhase != "denied" && previousPhase != "connection_lost" && previousPhase != "claim_unknown") || m.inFlight != 0 || m.connectionDone != nil {
		m.mu.Unlock()
		claimOutcome = "busy"
		return BeginControlResult{}, errControlBusy
	}
	m.phase = "acquiring"
	m.signalChangedLocked()
	m.mu.Unlock()

	identity, err := readControlIdentity(ctx, m.client, requestID)
	if err != nil {
		if previousPhase == "claim_unknown" {
			m.setAcquirePhase("claim_unknown")
		} else {
			m.resetAcquire()
		}
		return BeginControlResult{}, fmt.Errorf("check configured Ara profile: %w", err)
	}
	if previousPhase == "claim_unknown" {
		current, _, err := m.client.GetServerSessionWithRequestID(ctx, requestID)
		if err != nil {
			m.setAcquirePhase("claim_unknown")
			return BeginControlResult{}, fmt.Errorf("reconcile previous Ara control claim: %w", err)
		}
		if current.Connected && current.Hostname == m.clientHostname {
			m.setAcquirePhase("claim_unknown")
			claimOutcome = "unknown"
			return BeginControlResult{}, ControlClaimError{}
		}
	}
	if identity.profileID == "" {
		m.setAcquirePhase("denied")
		claimOutcome = "denied"
		return BeginControlResult{}, errors.New("begin control requires a configured active Ara profile")
	}

	session, claim, err := m.client.ConnectWithRequestID(ctx, m.clientHostname, nil, requestID)
	if err != nil {
		if claim.Outcome == ara.OutcomeUnknown {
			m.setAcquirePhase("claim_unknown")
			claimOutcome = "unknown"
			return BeginControlResult{}, ControlClaimError{}
		}
		if apiError, ok := errors.AsType[*ara.APIError](err); ok && apiError.Status == http.StatusConflict {
			claimOutcome = "denied"
			m.setAcquirePhase("denied")
		} else {
			m.resetAcquire()
		}
		return BeginControlResult{}, fmt.Errorf("claim Ara control session: %w", err)
	}
	connectionContext, connectionStop := context.WithCancel(m.root)
	stopOnToolCancel := context.AfterFunc(ctx, connectionStop)
	connection, err := m.client.OpenControlWebSocket(connectionContext, session)
	if err != nil {
		stopOnToolCancel()
		connectionStop()
		cleanupErr := m.releaseAfterFailedBind(ctx, session, requestID)
		m.resetAcquire()
		if cleanupErr != nil {
			m.setAcquirePhase("claim_unknown")
		}
		return BeginControlResult{}, errors.Join(fmt.Errorf("bind Ara control WebSocket: %w", err), cleanupErr)
	}
	if err := m.client.ResumeControlWebSocket(connectionContext, connection, identity.resumeToken); err != nil {
		stopOnToolCancel()
		connectionStop()
		_ = connection.CloseNow()
		cleanupErr := m.releaseAfterFailedBind(ctx, session, requestID)
		m.resetAcquire()
		if cleanupErr != nil {
			m.setAcquirePhase("claim_unknown")
		}
		return BeginControlResult{}, errors.Join(fmt.Errorf("resume Ara control event stream: %w", err), cleanupErr)
	}
	controlID := newControlID()
	if !stopOnToolCancel() || ctx.Err() != nil {
		connectionStop()
		_ = connection.CloseNow()
		cleanupErr := m.releaseAfterFailedBind(ctx, session, requestID)
		m.resetAcquire()
		if cleanupErr != nil {
			m.setAcquirePhase("claim_unknown")
		}
		return BeginControlResult{}, errors.Join(ctx.Err(), cleanupErr)
	}
	connectionDone := make(chan struct{})
	m.mu.Lock()
	if m.closed {
		m.phase = "not_owned"
		m.signalChangedLocked()
		m.mu.Unlock()
		connectionStop()
		_ = connection.CloseNow()
		return BeginControlResult{}, errors.Join(errControlBusy, m.releaseAfterFailedBind(ctx, session, requestID))
	}
	m.phase = "owned"
	m.controlID = controlID
	m.requestID = requestID
	m.identity = identity
	m.beginTrace = span.SpanContext()
	m.intents = make(map[string]*mutationIntent)
	m.ledgerBytes = 0
	m.session = session
	m.connection = connection
	m.connectionStop = connectionStop
	m.connectionDone = connectionDone
	m.lastHeartbeat = nil
	m.events = nil
	m.eventBytes = 0
	m.lastEventAt = nil
	m.lastEventSequence, _ = strconv.ParseInt(identity.resumeToken, 10, 64)
	m.droppedEvents = 0
	m.eventGap = false
	reconciled := time.Now().UTC()
	m.lastReconciliation = &reconciled
	m.socketActive = true
	m.signalChangedLocked()
	m.mu.Unlock()
	m.metrics.owned.Add(context.WithoutCancel(ctx), 1)
	m.metrics.websockets.Add(context.WithoutCancel(ctx), 1)

	claimOutcome = "granted"
	m.logger.InfoContext(ctx, "Ara control acquired", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_acquired", "transport", m.transport, "request_id", requestID, "websocket_state", "connected")
	go m.maintainConnection(connectionContext, connection, connectionDone)
	return BeginControlResult{ControlID: controlID, WebSocketState: "connected", ConnectedAt: session.ConnectedAt}, nil
}

// End invalidates the local capability before releasing Ara's session. It never
// stops or aborts Ara work.
func (m *ControlManager) End(ctx context.Context, controlID, requestID string) (retErr error) {
	if ctx == nil {
		return errors.New("control end context is required")
	}
	ctx, span := m.tracer.Start(ctx, "ara.control.end")
	defer span.End()
	defer func() {
		if retErr != nil {
			span.RecordError(errors.New("control end failed"))
			span.SetStatus(codes.Error, "control end failed")
		}
	}()
	m.mu.Lock()
	if controlID == "" || controlID != m.controlID || (m.phase != "owned" && m.phase != "reconnecting" && m.phase != "connection_lost") {
		m.mu.Unlock()
		return errControlID
	}
	if m.inFlight != 0 {
		m.mu.Unlock()
		return errControlBusy
	}
	m.phase = "ending"
	m.signalChangedLocked()
	session, connection := m.session, m.connection
	connectionStop, connectionDone := m.connectionStop, m.connectionDone
	socketActive := m.socketActive
	m.controlID = ""
	m.requestID = ""
	m.socketActive = false
	m.mu.Unlock()
	m.metrics.owned.Add(context.WithoutCancel(ctx), -1)
	if socketActive {
		m.metrics.websockets.Add(context.WithoutCancel(ctx), -1)
	}

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
	m.lastReconciliation = nil
	m.identity = controlIdentity{}
	m.beginTrace = trace.SpanContext{}
	m.intents = make(map[string]*mutationIntent)
	m.ledgerBytes = 0
	m.signalChangedLocked()
	m.mu.Unlock()
	resultErr := errors.Join(closeErr, waitErr, disconnectErr)
	if resultErr != nil {
		m.logger.WarnContext(ctx, "Ara control release incomplete", "service", "ara-mcp", "version", m.version,
			"component", "control", "event", "control_release_incomplete", "transport", m.transport,
			"request_id", requestID, "error_class", "cleanup")
		return resultErr
	}
	m.logger.InfoContext(ctx, "Ara control released", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_released", "transport", m.transport, "request_id", requestID)
	return nil
}

// Close releases a current session best-effort and stops manager-owned work.
func (m *ControlManager) Close(ctx context.Context, requestID string) error {
	if ctx == nil {
		return errors.New("control close context is required")
	}
	for {
		m.mu.Lock()
		if m.phase == "acquiring" || m.phase == "ending" || m.inFlight != 0 || (m.controlID == "" && m.connectionDone != nil) {
			changed := m.changed
			done := m.connectionDone
			m.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		m.closed = true
		controlID := m.controlID
		m.signalChangedLocked()
		m.mu.Unlock()
		if controlID == "" {
			m.stop()
			return nil
		}
		err := m.End(ctx, controlID, requestID)
		if errors.Is(err, errControlBusy) {
			continue
		}
		if errors.Is(err, errControlID) && m.Snapshot().ControlOwnership == "not_owned" {
			err = nil
		}
		m.stop()
		return err
	}
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
	case "reconnecting":
		ownership, websocketState = "reconnecting", "reconnecting"
	case "denied":
		ownership = "denied"
	case "claim_unknown":
		ownership = "unknown"
	case "connection_lost":
		ownership, websocketState = "connection_lost", "disconnected"
	case "ending":
		ownership, websocketState = "releasing", "closing"
	}
	status := ControlStatus{ControlOwnership: ownership, WebSocketState: websocketState, LastEventSequence: m.lastEventSequence, EventGap: m.eventGap, EventBacklog: len(m.events), DroppedEvents: m.droppedEvents}
	if m.lastHeartbeat != nil {
		last := *m.lastHeartbeat
		status.LastHeartbeat = &last
	}
	if m.lastReconciliation != nil {
		last := *m.lastReconciliation
		status.LastReconciliation = &last
	}
	if m.lastEventAt != nil {
		last := *m.lastEventAt
		status.LastEvent = &last
	}
	return status
}

// RecentEvents returns retained event metadata, never image or FITS bodies.
func (m *ControlManager) RecentEvents() EventSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	available := m.phase == "owned" || m.phase == "reconnecting"
	stale := !m.socketActive || m.lastHeartbeat == nil || time.Since(*m.lastHeartbeat) > time.Minute
	events := make([]ara.WebSocketEvent, len(m.events))
	for i, event := range m.events {
		if event.CurrentInstructionIndex != nil {
			event.CurrentInstructionIndex = new(*event.CurrentInstructionIndex)
		}
		if event.FailedInstructionIndex != nil {
			event.FailedInstructionIndex = new(*event.FailedInstructionIndex)
		}
		events[i] = event
	}
	return EventSnapshot{Available: available, Stale: stale, Gap: m.eventGap, LastSequence: m.lastEventSequence, Dropped: m.droppedEvents, Events: events}
}

// Require rejects mutations unless the caller presents the live phase ID.
func (m *ControlManager) Require(controlID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.phase != "owned" || controlID == "" || controlID != m.controlID {
		return errControlID
	}
	return nil
}

// DispatchMutation arbitrates one mutation call. The handler must perform only
// one upstream dispatch and return a bounded receipt; it must not retain bodies in
// the receipt. Run flags and IDs are fresh observations supplied by the tool.
func (m *ControlManager) DispatchMutation(ctx context.Context, request MutationRequest, dispatch func(context.Context) (MutationReceipt, error)) (MutationReceipt, error) {
	if ctx == nil || dispatch == nil || !validIntentID(request.IntentID) || !validIntentID(request.Operation) || len(request.Arguments) > maxMutationArgumentBytes {
		return MutationReceipt{}, errInvalidIntent
	}
	m.mu.Lock()
	owned := !m.closed && m.phase == "owned" && request.ControlID != "" && request.ControlID == m.controlID
	m.mu.Unlock()
	if !owned {
		return MutationReceipt{}, errControlID
	}
	switch request.Kind {
	case MutationNormal, MutationManual, MutationStartRun, MutationRunControl, MutationInterrupt:
	default:
		return MutationReceipt{}, errInvalidIntent
	}
	needsRunSnapshot := request.Kind == MutationManual || request.Kind == MutationStartRun || request.Kind == MutationRunControl ||
		(request.Kind == MutationInterrupt && request.ExpectedRunID != "")
	if needsRunSnapshot && request.Preflight == nil {
		return MutationReceipt{}, errRunStateUnknown
	}
	if (request.Kind == MutationRunControl || request.Kind == MutationInterrupt && request.Preflight != nil) && request.ExpectedRunID == "" {
		return MutationReceipt{}, errStaleRunID
	}
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(request.Kind))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(request.Operation))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(request.ExpectedRunID))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(request.Arguments)
	var digest [sha256.Size]byte
	copy(digest[:], hasher.Sum(nil))

	m.mu.Lock()
	if m.closed || m.phase != "owned" || request.ControlID == "" || request.ControlID != m.controlID {
		m.mu.Unlock()
		return MutationReceipt{}, errControlID
	}
	if existing := m.intents[request.IntentID]; existing != nil {
		if existing.arguments != digest {
			m.mu.Unlock()
			return MutationReceipt{}, errIntentConflict
		}
		select {
		case <-existing.done:
			m.mu.Unlock()
			receipt := existing.receipt
			receipt.Replayed = true
			m.metrics.replays.Add(context.WithoutCancel(ctx), 1)
			if receipt.ErrorClass != "" {
				return receipt, &MutationReplayError{ErrorClass: receipt.ErrorClass, Receipt: receipt}
			}
			return receipt, nil
		default:
		}
		if existing.waiters >= maxIntentWaiters {
			m.mu.Unlock()
			return MutationReceipt{}, errMutationBusy
		}
		existing.waiters++
		m.mu.Unlock()
		receipt, err := waitMutationIntent(ctx, existing)
		m.mu.Lock()
		existing.waiters--
		m.mu.Unlock()
		if receipt.Replayed {
			m.metrics.replays.Add(context.WithoutCancel(ctx), 1)
		}
		return receipt, err
	}
	entryBytes := len(request.IntentID) + sha256.Size + mutationReceiptBudget
	tracked := true
	if len(m.intents) >= maxMutationIntents || m.ledgerBytes+entryBytes > maxMutationLedgerBytes {
		if request.Kind != MutationInterrupt {
			m.mu.Unlock()
			return MutationReceipt{}, errIntentCapacity
		}
		// ponytail: receipt-ledger overflow interrupts bypass replay tracking so
		// stop remains available; add a separate bounded interrupt ledger if needed.
		tracked = false
	}
	var entry *mutationIntent
	if tracked {
		entry = &mutationIntent{arguments: digest, done: make(chan struct{})}
		m.intents[request.IntentID] = entry
		m.ledgerBytes += entryBytes
	}
	m.mu.Unlock()

	lane := m.normal
	if request.Kind == MutationInterrupt {
		lane = m.interrupt
	}
	select {
	case lane <- struct{}{}:
		defer func() { <-lane }()
	default:
		if tracked {
			m.forgetMutationIntent(request.IntentID, entry, MutationReceipt{Outcome: ara.OutcomeFailed, ErrorClass: "busy"})
		}
		return MutationReceipt{}, errMutationBusy
	}

	m.mu.Lock()
	if m.phase != "owned" || request.ControlID != m.controlID {
		m.mu.Unlock()
		if tracked {
			m.forgetMutationIntent(request.IntentID, entry, MutationReceipt{Outcome: ara.OutcomeFailed, ErrorClass: "control_lost"})
		}
		return MutationReceipt{}, errControlID
	}
	m.inFlight++
	m.signalChangedLocked()
	m.mu.Unlock()
	if needsRunSnapshot {
		snapshot, err := request.Preflight(ctx)
		if err != nil {
			return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, toolErrorClass(err), fmt.Errorf("refresh Ara run state: %w", err))
		}
		if !snapshot.Known {
			return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, "unknown", errRunStateUnknown)
		}
		if (request.Kind == MutationManual || request.Kind == MutationStartRun) && snapshot.ActiveRun {
			return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, "run_active", errRunActive)
		}
		if request.Kind == MutationRunControl || request.Kind == MutationInterrupt && request.ExpectedRunID != "" {
			if !snapshot.ActiveRun {
				return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, "run_not_active", errRunInactive)
			}
			if snapshot.RunID != request.ExpectedRunID {
				return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, "stale_run_id", errStaleRunID)
			}
		}
		m.mu.Lock()
		stillOwned := !m.closed && m.phase == "owned" && request.ControlID == m.controlID
		m.mu.Unlock()
		if !stillOwned {
			return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, "control_lost", errControlID)
		}
	}
	if err := m.verifyControlSession(ctx, request.ControlID); err != nil {
		return m.rejectUndispatchedMutation(request.IntentID, entry, tracked, toolErrorClass(err), err)
	}

	receipt, dispatchErr := dispatch(ctx)
	if dispatchErr != nil {
		if receipt.Outcome != ara.OutcomeFailed && receipt.Outcome != ara.OutcomeUnknown {
			receipt.Outcome = ara.OutcomeUnknown
			receipt.RetrySafe = false
		}
		if receipt.ErrorClass == "" {
			receipt.ErrorClass = toolErrorClass(dispatchErr)
		}
	} else if receipt.Outcome == "" {
		receipt.Outcome = ara.OutcomeCompleted
	}
	if !validMutationOutcome(receipt.Outcome) {
		receipt.Outcome = ara.OutcomeUnknown
		receipt.RetrySafe = false
		receipt.ErrorClass = "invalid_receipt"
	}
	if receipt.ReceiptID != "" && !validIntentID(receipt.ReceiptID) {
		receipt.ReceiptID = ""
		receipt.Outcome = ara.OutcomeUnknown
		receipt.RetrySafe = false
		receipt.ErrorClass = "invalid_receipt"
	}
	if receipt.ErrorClass != "" {
		receipt.ErrorClass = boundedMutationErrorClass(receipt.ErrorClass)
	}

	m.mu.Lock()
	m.inFlight--
	if tracked {
		entry.receipt = receipt
		close(entry.done)
	}
	m.signalChangedLocked()
	m.mu.Unlock()
	attrs := metric.WithAttributes(
		attribute.String("ara.mutation.kind", string(request.Kind)),
		attribute.String("ara.outcome", string(receipt.Outcome)),
		attribute.String("error.type", boundedMutationErrorClass(receipt.ErrorClass)),
	)
	m.metrics.mutations.Add(context.WithoutCancel(ctx), 1, attrs)
	level := slog.LevelInfo
	if dispatchErr != nil || receipt.Outcome == ara.OutcomeUnknown || receipt.ErrorClass != "" {
		level = slog.LevelWarn
	}
	if dispatchErr == nil {
		m.logger.Log(ctx, level, "Ara control mutation dispatched", "service", "ara-mcp", "version", m.version,
			"component", "control", "event", "mutation_dispatched", "transport", m.transport,
			"request_id", requestID(ctx),
			"outcome", receipt.Outcome, "error_class", boundedMutationErrorClass(receipt.ErrorClass))
	}
	if dispatchErr != nil {
		return receipt, &MutationDispatchError{Receipt: receipt, cause: dispatchErr}
	}
	return receipt, nil
}

func waitMutationIntent(ctx context.Context, entry *mutationIntent) (MutationReceipt, error) {
	select {
	case <-entry.done:
	case <-ctx.Done():
		return MutationReceipt{}, ctx.Err()
	}
	receipt := entry.receipt
	receipt.Replayed = true
	if receipt.ErrorClass != "" {
		return receipt, &MutationReplayError{ErrorClass: receipt.ErrorClass, Receipt: receipt}
	}
	return receipt, nil
}

func (m *ControlManager) forgetMutationIntent(intentID string, entry *mutationIntent, receipt MutationReceipt) {
	m.mu.Lock()
	if m.intents[intentID] == entry {
		delete(m.intents, intentID)
		m.ledgerBytes -= len(intentID) + sha256.Size + mutationReceiptBudget
	}
	entry.receipt = receipt
	close(entry.done)
	m.mu.Unlock()
}

func (m *ControlManager) rejectUndispatchedMutation(intentID string, entry *mutationIntent, tracked bool, errorClass string, err error) (MutationReceipt, error) {
	class := boundedMutationErrorClass(errorClass)
	receipt := MutationReceipt{Outcome: ara.OutcomeFailed, ErrorClass: class}
	m.mu.Lock()
	m.inFlight--
	if tracked {
		if m.intents[intentID] == entry {
			delete(m.intents, intentID)
			m.ledgerBytes -= len(intentID) + sha256.Size + mutationReceiptBudget
		}
		entry.receipt = receipt
		close(entry.done)
	}
	m.signalChangedLocked()
	m.mu.Unlock()
	return receipt, err
}

func validIntentID(value string) bool {
	if value == "" || len(value) > maxMutationIntentIDBytes {
		return false
	}
	for _, r := range value {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func boundedMutationErrorClass(value string) string {
	switch value {
	case "", "none":
		return "none"
	case "busy", "control_lost", "invalid_receipt", "intent_conflict", "run_state_unknown", "timeout", "cancelled", "network", "decode",
		"response_too_large", "invalid_argument", "invalid_upstream_request", "not_found", "upstream_conflict", "upstream_unavailable",
		"invalid_upstream_response", "upstream_error", "uncertain", "run_active", "run_not_active", "stale_run_id", "equipment_unavailable", "unsupported_capability", "adapter_error":
		return value
	default:
		return "other"
	}
}

func validMutationOutcome(outcome ara.Outcome) bool {
	switch outcome {
	case ara.OutcomeFailed, ara.OutcomeCompleted, ara.OutcomeAccepted, ara.OutcomeUnknown:
		return true
	default:
		return false
	}
}

// MutationDispatchError reports an uncertain/failed mutation without exposing
// upstream text; the underlying error remains available for errors.Is/As.
type MutationDispatchError struct {
	Receipt MutationReceipt
	cause   error
}

func (e *MutationDispatchError) Error() string {
	return "mutation dispatch returned " + string(e.Receipt.Outcome) + " outcome"
}

func (e *MutationDispatchError) Unwrap() error { return e.cause }

// MutationReplayError carries a bounded class and receipt; raw errors and request
// arguments are not retained in the phase ledger.
type MutationReplayError struct {
	ErrorClass string
	Receipt    MutationReceipt
}

func (e *MutationReplayError) Error() string { return "mutation outcome replayed: " + e.ErrorClass }

func (m *ControlManager) maintainConnection(ctx context.Context, connection *websocket.Conn, done chan struct{}) {
	defer func() {
		close(done)
		m.mu.Lock()
		if m.connectionDone == done {
			m.connectionDone = nil
			m.signalChangedLocked()
		}
		m.mu.Unlock()
	}()
	for current := connection; ; {
		_ = m.client.MaintainControlWebSocketWithEvents(ctx, current, func(at time.Time) {
			m.mu.Lock()
			if m.connection == current && m.phase == "owned" {
				last := at
				m.lastHeartbeat = &last
				m.metrics.heartbeats.Add(ctx, 1)
			}
			m.mu.Unlock()
		}, func() {
			m.logger.WarnContext(ctx, "Ara control takeover rejected", "service", "ara-mcp", "version", m.version, "component", "control", "event", "control_takeover_rejected", "transport", m.transport, "request_id", m.controlRequestID(current))
		}, m.recordEvent)
		if ctx.Err() != nil || !m.markConnectionLost(current) {
			return
		}
		reconnected, ok := m.reconnectControlSession(ctx, current)
		if !ok {
			m.invalidateControl(current)
			return
		}
		current = reconnected
	}
}

func (m *ControlManager) recordEvent(event ara.WebSocketEvent) {
	size := recentEventMetadataBytes
	m.mu.Lock()
	if event.Gap {
		m.eventGap = true
		if event.Seq > m.lastEventSequence {
			m.droppedEvents++
			m.lastEventSequence = event.Seq
			now := time.Now().UTC()
			m.lastEventAt = &now
		}
		m.mu.Unlock()
		m.metrics.events.Add(context.Background(), 1, metric.WithAttributes(attribute.String("category", "recovery"), attribute.String("outcome", "gap")))
		return
	}
	if event.Seq <= m.lastEventSequence {
		m.mu.Unlock()
		return
	}
	if m.lastEventSequence > 0 && event.Seq > m.lastEventSequence+1 {
		m.eventGap = true
		m.droppedEvents += event.Seq - m.lastEventSequence - 1
	}
	m.lastEventSequence = event.Seq
	now := time.Now().UTC()
	m.lastEventAt = &now
	if size > maxRecentEventBytes {
		m.droppedEvents++
		m.eventGap = true
		m.mu.Unlock()
		m.metrics.events.Add(context.Background(), 1, metric.WithAttributes(attribute.String("category", eventCategory(event.Type)), attribute.String("outcome", "oversize")))
		return
	}
	m.events = append(m.events, event)
	m.eventBytes += size
	for len(m.events) > maxRecentEvents || m.eventBytes > maxRecentEventBytes {
		m.eventBytes -= recentEventMetadataBytes
		m.events[0] = ara.WebSocketEvent{}
		m.events = m.events[1:]
		m.droppedEvents++
		m.eventGap = true
	}
	m.mu.Unlock()
	m.metrics.events.Add(context.Background(), 1, metric.WithAttributes(attribute.String("category", eventCategory(event.Type)), attribute.String("outcome", "processed")))
}

func eventCategory(eventType string) string {
	switch {
	case strings.HasPrefix(eventType, "sequence."):
		return "sequence"
	case strings.HasPrefix(eventType, "equipment.") || strings.HasPrefix(eventType, "camera."):
		return "equipment"
	case strings.HasPrefix(eventType, "autofocus.") || strings.HasPrefix(eventType, "session.") || strings.HasPrefix(eventType, "calibration."):
		return "job"
	case strings.HasPrefix(eventType, "frame."):
		return "frame"
	default:
		return "other"
	}
}

func (m *ControlManager) markConnectionLost(connection *websocket.Conn) bool {
	m.mu.Lock()
	if m.connection != connection || m.phase != "owned" {
		m.mu.Unlock()
		return false
	}
	m.phase = "reconnecting"
	socketWasActive := m.socketActive
	m.socketActive = false
	requestID := m.requestID
	m.signalChangedLocked()
	m.mu.Unlock()
	if socketWasActive {
		m.metrics.websockets.Add(context.Background(), -1)
	}
	m.logger.Warn("Ara control WebSocket lost; recovery started", "service", "ara-mcp", "version", m.version,
		"component", "control", "event", "connection_lost", "transport", m.transport,
		"request_id", requestID, "error_class", "websocket")
	return true
}

func (m *ControlManager) verifyControlSession(ctx context.Context, controlID string) error {
	m.mu.Lock()
	if m.closed || m.phase != "owned" || controlID == "" || controlID != m.controlID {
		m.mu.Unlock()
		return errControlID
	}
	baseline, session, connection := m.identity, m.session, m.connection
	requestID := requestID(ctx)
	m.mu.Unlock()

	current, err := readControlIdentity(ctx, m.client, requestID)
	if err != nil {
		return fmt.Errorf("verify Ara control identity: %w", err)
	}
	if !baseline.sameDaemon(current) || baseline.profileID != current.profileID {
		m.invalidateActiveControl(connection)
		return errControlID
	}
	currentSession, _, err := m.client.GetServerSessionWithRequestID(ctx, requestID)
	if err != nil {
		if apiError, ok := errors.AsType[*ara.APIError](err); ok && (apiError.Status == http.StatusNotFound || apiError.Status == http.StatusUnauthorized || apiError.Status == http.StatusForbidden) {
			m.invalidateActiveControl(connection)
			return errControlID
		}
		return fmt.Errorf("verify Ara control session: %w", err)
	}
	if !currentSession.Connected || currentSession.Hostname != session.Hostname || currentSession.IdleSeconds == nil || *currentSession.IdleSeconds < 0 || *currentSession.IdleSeconds >= 60 {
		m.invalidateActiveControl(connection)
		return errControlID
	}
	m.mu.Lock()
	if m.phase != "owned" || m.controlID != controlID || m.connection != connection {
		m.mu.Unlock()
		return errControlID
	}
	m.identity = current
	reconciled := time.Now().UTC()
	m.lastReconciliation = &reconciled
	m.mu.Unlock()
	return nil
}

func (m *ControlManager) invalidateActiveControl(connection *websocket.Conn) {
	m.mu.Lock()
	if m.phase != "owned" || m.connection != connection {
		m.mu.Unlock()
		return
	}
	m.phase = "reconnecting"
	socketActive := m.socketActive
	m.socketActive = false
	m.signalChangedLocked()
	m.mu.Unlock()
	if socketActive {
		m.metrics.websockets.Add(context.Background(), -1)
	}
	m.invalidateControl(connection)
}

func (m *ControlManager) reconnectControlSession(ctx context.Context, oldConnection *websocket.Conn) (reconnected *websocket.Conn, recovered bool) {
	recoveryContext, cancel := context.WithTimeout(ctx, m.reconnectWindow)
	defer cancel()
	m.mu.Lock()
	beginTrace := m.beginTrace
	m.mu.Unlock()
	recoveryContext, span := m.tracer.Start(recoveryContext, "ara.control.reconnect", trace.WithLinks(trace.Link{SpanContext: beginTrace}))
	defer func() {
		if !recovered && ctx.Err() == nil {
			span.RecordError(errors.New("control recovery failed"))
			span.SetStatus(codes.Error, "control recovery failed")
		}
		span.End()
	}()
	delay := m.reconnectInterval
	for {
		if recoveryContext.Err() != nil {
			return nil, false
		}
		m.metrics.reconnects.Add(recoveryContext, 1)
		m.mu.Lock()
		identity, session, requestID := m.identity, m.session, m.requestID
		m.mu.Unlock()

		requestContext, requestCancel := context.WithTimeout(recoveryContext, 5*time.Second)
		current, err := readControlIdentity(requestContext, m.client, requestID)
		requestCancel()
		if err == nil && (!identity.sameDaemon(current) || identity.profileID != current.profileID) {
			return nil, false
		}
		if err == nil {
			requestContext, requestCancel = context.WithTimeout(recoveryContext, 5*time.Second)
			sessionInfo, _, sessionErr := m.client.GetServerSessionWithRequestID(requestContext, requestID)
			err = sessionErr
			requestCancel()
			if err == nil && (!sessionInfo.Connected || sessionInfo.Hostname != session.Hostname || sessionInfo.IdleSeconds == nil || *sessionInfo.IdleSeconds < 0 || *sessionInfo.IdleSeconds >= 60) {
				return nil, false
			}
			if err == nil {
				requestContext, requestCancel = context.WithTimeout(recoveryContext, 5*time.Second)
				_, _, err = m.client.ListSequencesWithRequestID(requestContext, 100, requestID)
				requestCancel()
			}
			if err == nil {
				sessionID := session.SessionID()
				reclaimed, _, reclaimErr := m.client.ConnectWithRequestID(recoveryContext, m.clientHostname, &sessionID, requestID)
				if reclaimErr == nil && reclaimed.SessionID() != sessionID {
					cleanupContext, cleanupCancel := context.WithTimeout(context.WithoutCancel(recoveryContext), 5*time.Second)
					_, _ = m.client.DisconnectWithRequestID(cleanupContext, reclaimed, requestID)
					cleanupCancel()
					return nil, false
				}
				if reclaimErr == nil {
					var connection *websocket.Conn
					connection, reclaimErr = m.client.OpenControlWebSocket(recoveryContext, reclaimed)
					if reclaimErr == nil {
						m.mu.Lock()
						resumeCursor := m.lastEventSequence
						m.mu.Unlock()
						resumeToken := current.resumeToken
						if resumeCursor > 0 {
							resumeToken = strconv.FormatInt(resumeCursor, 10)
						}
						reclaimErr = m.client.ResumeControlWebSocket(recoveryContext, connection, resumeToken)
					}
					if reclaimErr == nil {
						m.mu.Lock()
						if m.phase == "reconnecting" && m.connection == oldConnection && recoveryContext.Err() == nil {
							m.connection = connection
							m.session = reclaimed
							m.identity = current
							m.phase = "owned"
							m.socketActive = true
							reconciled := time.Now().UTC()
							m.lastReconciliation = &reconciled
							m.signalChangedLocked()
							m.mu.Unlock()
							m.metrics.websockets.Add(context.WithoutCancel(recoveryContext), 1)
							m.logger.Info("Ara control WebSocket recovered", "service", "ara-mcp", "version", m.version,
								"component", "control", "event", "connection_recovered", "transport", m.transport,
								"request_id", requestID)
							recovered = true
							return connection, true
						}
						m.mu.Unlock()
						_ = connection.CloseNow()
						return nil, false
					}
					if connection != nil {
						_ = connection.CloseNow()
					}
					if !retryControlError(reclaimErr) {
						return nil, false
					}
				} else if !retryControlError(reclaimErr) {
					return nil, false
				}
			} else if !retryControlError(err) {
				return nil, false
			}
		} else if !retryControlError(err) {
			return nil, false
		}
		timer := time.NewTimer(delay)
		select {
		case <-recoveryContext.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		delay = min(delay*2, m.reconnectMaxInterval)
	}
}

func retryControlError(err error) bool {
	if err == nil {
		return false
	}
	if apiError, ok := errors.AsType[*ara.APIError](err); ok {
		return apiError.Status >= http.StatusInternalServerError
	}
	if handshake, ok := errors.AsType[*ara.WebSocketHandshakeError](err); ok {
		return handshake.StatusCode >= http.StatusInternalServerError
	}
	if requestError, ok := errors.AsType[*ara.RequestError](err); ok {
		return requestError.Class == "network" || requestError.Class == "timeout"
	}
	return true // websocket dial failures are sanitized network errors
}

func (m *ControlManager) invalidateControl(connection *websocket.Conn) {
	m.mu.Lock()
	if m.connection != connection || m.phase != "reconnecting" {
		m.mu.Unlock()
		return
	}
	session := m.session
	requestID := m.requestID
	stop := m.connectionStop
	m.phase = "ending"
	m.controlID = ""
	m.requestID = ""
	m.signalChangedLocked()
	m.mu.Unlock()
	if stop != nil {
		stop()
	}
	m.metrics.owned.Add(context.Background(), -1)
	cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, disconnectErr := m.client.DisconnectWithRequestID(cleanupContext, session, requestID)
	cancel()
	if apiError, ok := errors.AsType[*ara.APIError](disconnectErr); ok && apiError.Status == http.StatusNotFound {
		disconnectErr = nil
	}
	m.mu.Lock()
	m.phase = "connection_lost"
	m.session = ara.ControlSession{}
	m.connection = nil
	m.connectionStop = nil
	m.socketActive = false
	m.identity = controlIdentity{}
	m.intents = make(map[string]*mutationIntent)
	m.ledgerBytes = 0
	m.signalChangedLocked()
	m.mu.Unlock()
	m.logger.Warn("Ara control invalidated after recovery failed", "service", "ara-mcp", "version", m.version,
		"component", "control", "event", "control_invalidated", "transport", m.transport, "request_id", requestID,
		"error_class", "session_or_server_changed")
	if disconnectErr != nil {
		m.logger.Warn("Ara control session release after invalidation failed", "service", "ara-mcp", "version", m.version,
			"component", "control", "event", "control_release_failed", "transport", m.transport,
			"request_id", requestID, "error_class", toolErrorClass(disconnectErr))
	}
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
	m.setAcquirePhase("not_owned")
}

func (m *ControlManager) setAcquirePhase(phase string) {
	m.mu.Lock()
	m.phase = phase
	m.signalChangedLocked()
	m.mu.Unlock()
}

func (m *ControlManager) signalChangedLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *ControlManager) releaseAfterFailedBind(ctx context.Context, session ara.ControlSession, requestID string) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, err := m.client.DisconnectWithRequestID(cleanupContext, session, requestID)
	m.resetAcquire()
	return err
}

func newControlID() string { return uuid.NewV4().String() }
