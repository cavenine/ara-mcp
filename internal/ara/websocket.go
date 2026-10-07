// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const araWebSocketVersion = "1"

// WebSocketEvent is a bounded context projection of Ara's event stream.
type WebSocketEvent struct {
	Type                    string                 `json:"type"`
	Timestamp               string                 `json:"ts,omitempty"`
	Seq                     int64                  `json:"seq"`
	Gap                     bool                   `json:"gap,omitzero"`
	SequenceID              string                 `json:"sequence_id,omitempty"`
	RunID                   string                 `json:"run_id,omitempty"`
	JobID                   string                 `json:"job_id,omitempty"`
	FrameID                 string                 `json:"frame_id,omitempty"`
	DeviceType              string                 `json:"device_type,omitempty"`
	DeviceID                string                 `json:"device_id,omitempty"`
	DeviceName              string                 `json:"device_name,omitempty"`
	Kind                    string                 `json:"kind,omitempty"`
	Details                 string                 `json:"details,omitempty"`
	Action                  string                 `json:"action,omitempty"`
	DetectedUTC             string                 `json:"detected_utc,omitempty"`
	Removed                 bool                   `json:"removed,omitzero"`
	State                   string                 `json:"state,omitempty"`
	InstructionsCompleted   int                    `json:"instructions_completed"`
	InstructionsTotal       int                    `json:"instructions_total"`
	CurrentInstructionIndex *int                   `json:"current_instruction_index,omitempty"`
	FailedInstructionIndex  *int                   `json:"failed_instruction_index,omitempty"`
	FailedInstructionName   string                 `json:"failed_instruction_name,omitempty"`
	FailureReason           string                 `json:"failure_reason,omitempty"`
	Exposure                *ExposureEventContext  `json:"exposure,omitempty"`
	Guider                  *GuiderEventContext    `json:"guider,omitempty"`
	Autofocus               *AutofocusEventContext `json:"autofocus,omitempty"`
}

// ExposureEventContext is the bounded payload subset for camera exposure lifecycle events.
type ExposureEventContext struct {
	FrameID     string   `json:"frame_id,omitempty"`
	ExposureSec *float64 `json:"exposure_sec,omitempty"`
	StartedUTC  string   `json:"started_utc,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	FilterName  string   `json:"filter_name,omitempty"`
	ElapsedMS   *int64   `json:"elapsed_ms,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

// GuiderEventContext retains bounded guide-step measurements and session markers.
type GuiderEventContext struct {
	Kind             string   `json:"kind,omitempty"`
	Frame            *int     `json:"frame,omitempty"`
	TimeSec          *float64 `json:"time_sec,omitempty"`
	RARawPX          *float64 `json:"ra_raw_px,omitempty"`
	DecRawPX         *float64 `json:"dec_raw_px,omitempty"`
	RAArcsec         *float64 `json:"ra_arcsec,omitempty"`
	DecArcsec        *float64 `json:"dec_arcsec,omitempty"`
	RADurationMS     *float64 `json:"ra_duration_ms,omitempty"`
	DecDurationMS    *float64 `json:"dec_duration_ms,omitempty"`
	PixelScaleArcsec *float64 `json:"pixel_scale_arcsec,omitempty"`
	StarMass         *float64 `json:"star_mass,omitempty"`
	SNR              *float64 `json:"snr,omitempty"`
	DXPX             *float64 `json:"dx_px,omitempty"`
	DYPX             *float64 `json:"dy_px,omitempty"`
	DistancePX       *float64 `json:"distance_px,omitempty"`
	SettleTimeSec    *float64 `json:"settle_time_sec,omitempty"`
	Status           *int     `json:"status,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// AutofocusEventContext retains bounded probe, fit, and run lifecycle details.
type AutofocusEventContext struct {
	Mode             string   `json:"mode,omitempty"`
	Phase            string   `json:"phase,omitempty"`
	Algorithm        string   `json:"algorithm,omitempty"`
	Reason           string   `json:"reason,omitempty"`
	Severity         string   `json:"severity,omitempty"`
	StepIndex        *int     `json:"step_index,omitempty"`
	ShotIndex        *int     `json:"shot_index,omitempty"`
	Position         *int     `json:"position,omitempty"`
	Stars            *int     `json:"stars,omitempty"`
	StarsUsed        *int     `json:"stars_used,omitempty"`
	TotalSteps       *int     `json:"total_steps,omitempty"`
	FinalPosition    *int     `json:"final_position,omitempty"`
	FinalStars       *int     `json:"final_stars,omitempty"`
	Probes           *int     `json:"probes,omitempty"`
	RestoredPosition *int     `json:"restored_position,omitempty"`
	Kept             *bool    `json:"kept,omitempty"`
	Usable           *bool    `json:"usable,omitempty"`
	WithinRange      *bool    `json:"within_range,omitempty"`
	HFR              *float64 `json:"hfr,omitempty"`
	RSquared         *float64 `json:"r_squared,omitempty"`
	BestPosition     *float64 `json:"best_position,omitempty"`
	PredictedHFR     *float64 `json:"predicted_hfr,omitempty"`
	FinalHFR         *float64 `json:"final_hfr,omitempty"`
	DurationSeconds  *float64 `json:"duration_seconds,omitempty"`
	OffsetPercent    *float64 `json:"offset_percent,omitempty"`
	DirectionDegrees *float64 `json:"direction_degrees,omitempty"`
}

type websocketEventPayload struct {
	SequenceID              string `json:"sequence_id"`
	RunID                   string `json:"run_id"`
	JobID                   string `json:"job_id"`
	FrameID                 string `json:"frame_id"`
	DeviceType              string `json:"device_type"`
	DeviceID                string `json:"device_id"`
	DeviceName              string `json:"device_name"`
	Kind                    string `json:"kind"`
	Details                 string `json:"details"`
	Action                  string `json:"action"`
	DetectedUTC             string `json:"detected_utc"`
	Removed                 bool   `json:"removed"`
	State                   string `json:"state"`
	InstructionsCompleted   int    `json:"instructions_completed"`
	InstructionsTotal       int    `json:"instructions_total"`
	CurrentInstructionIndex *int   `json:"current_instruction_index"`
	FailedInstructionIndex  *int   `json:"failed_instruction_index"`
	FailedInstructionName   string `json:"failed_instruction_name"`
	FailureReason           string `json:"failure_reason"`
}

// WebSocketHandshakeError contains only the HTTP status, never the server body
// or session capability.
type WebSocketHandshakeError struct{ StatusCode int }

func (e *WebSocketHandshakeError) Error() string {
	return fmt.Sprintf("ara websocket: handshake failed with http status %d", e.StatusCode)
}

// OpenControlWebSocket binds Ara's event socket to this adapter-owned session.
// The caller owns the returned connection and must close it when the control
// phase ends. Unbound monitoring sockets are deliberately not supported here.
func (c *Client) OpenControlWebSocket(ctx context.Context, session ControlSession) (*websocket.Conn, error) {
	if ctx == nil {
		return nil, errors.New("ara websocket: context is required")
	}
	if session.sessionID == "" {
		return nil, errors.New("ara websocket: control session is required")
	}
	endpoint, err := controlWebSocketURL(c.gateway.baseURL)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: c.gateway.timeout}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   c.gateway.timeout,
		ResponseHeaderTimeout: c.gateway.timeout,
	}
	defer transport.CloseIdleConnections()
	header := make(http.Header)
	header.Set("X-Ara-Session", session.sessionID)
	header.Set("X-Ara-WS-Version", araWebSocketVersion)
	connection, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{
		HTTPHeader: header,
		HTTPClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	})
	if err == nil {
		return connection, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if response != nil {
		return nil, &WebSocketHandshakeError{StatusCode: response.StatusCode}
	}
	return nil, errors.New("ara websocket: dial failed")
}

// ResumeControlWebSocket sends Ara's resume cursor as the optional first text
// frame. An empty token explicitly requests a fresh event subscription.
func (c *Client) ResumeControlWebSocket(ctx context.Context, conn *websocket.Conn, token string) error {
	if ctx == nil || conn == nil || len(token) > 128 || (token != "" && !validHeaderValue(token, 128)) {
		return errors.New("ara websocket: invalid resume request")
	}
	frame, err := json.Marshal(struct {
		ResumeToken string `json:"resume_token"`
	}{ResumeToken: token})
	if err != nil {
		return fmt.Errorf("ara websocket: encode resume request: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		return errors.New("ara websocket: resume request failed")
	}
	return nil
}

// MaintainControlWebSocket reads one session-bound socket, answers Ara's
// application-level heartbeat, and rejects takeover requests so a human holder
// is not displaced. Other event frames are reserved for the T08 event consumer.
func (c *Client) MaintainControlWebSocket(ctx context.Context, conn *websocket.Conn, onHeartbeat func(time.Time), onTakeoverRejected func()) error {
	return c.MaintainControlWebSocketWithEvents(ctx, conn, onHeartbeat, onTakeoverRejected, nil)
}

// MaintainControlWebSocketWithEvents also forwards Ara's sequenced event envelopes.
func (c *Client) MaintainControlWebSocketWithEvents(ctx context.Context, conn *websocket.Conn, onHeartbeat func(time.Time), onTakeoverRejected func(), onEvent func(WebSocketEvent)) error {
	if ctx == nil || conn == nil {
		return errors.New("ara websocket: context and connection are required")
	}
	conn.SetReadLimit(defaultMaxResponseBytes)
	for {
		messageType, payload, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("ara websocket: read failed")
		}
		if messageType != websocket.MessageText {
			continue
		}
		var frame struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
		}
		if err := json.Unmarshal(payload, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "ping":
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"pong"}`)); err != nil {
				return errors.New("ara websocket: heartbeat response failed")
			}
			if onHeartbeat != nil {
				onHeartbeat(time.Now().UTC())
			}
		case "connection.request":
			if frame.RequestID == "" || len(frame.RequestID) > 128 {
				continue
			}
			response, err := json.Marshal(struct {
				Type      string `json:"type"`
				RequestID string `json:"request_id"`
				Action    string `json:"action"`
			}{Type: "connection.response", RequestID: frame.RequestID, Action: "reject"})
			if err != nil {
				return fmt.Errorf("ara websocket: encode takeover response: %w", err)
			}
			if err := conn.Write(ctx, websocket.MessageText, response); err != nil {
				return errors.New("ara websocket: takeover response failed")
			}
			if onTakeoverRejected != nil {
				onTakeoverRejected()
			}
		default:
			if onEvent != nil {
				var header struct {
					Type string `json:"type"`
					Seq  int64  `json:"seq"`
				}
				headerErr := json.Unmarshal(payload, &header)
				if headerErr == nil && header.Type != "" && header.Seq > 0 {
					var envelope struct {
						Type      string         `json:"type"`
						Timestamp string         `json:"ts"`
						Seq       int64          `json:"seq"`
						Payload   jsontext.Value `json:"payload"`
					}
					if err := json.Unmarshal(payload, &envelope); err != nil || len(header.Type) > 128 || envelope.Type != header.Type || envelope.Seq != header.Seq {
						onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: header.Seq, Gap: true})
						continue
					}
					var identity websocketEventPayload
					if envelope.Payload.Kind() != '{' || json.Unmarshal(envelope.Payload, &identity) != nil || invalidEventIdentity(envelope.Timestamp, identity) {
						onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: envelope.Seq, Gap: true})
						continue
					}
					event := WebSocketEvent{Type: envelope.Type, Timestamp: envelope.Timestamp, Seq: envelope.Seq, SequenceID: identity.SequenceID, RunID: identity.RunID, JobID: identity.JobID, FrameID: identity.FrameID, DeviceType: identity.DeviceType, DeviceID: identity.DeviceID, DeviceName: identity.DeviceName, Kind: identity.Kind, Details: identity.Details, Action: identity.Action, DetectedUTC: identity.DetectedUTC, Removed: identity.Removed, State: identity.State, InstructionsCompleted: identity.InstructionsCompleted, InstructionsTotal: identity.InstructionsTotal, CurrentInstructionIndex: identity.CurrentInstructionIndex, FailedInstructionIndex: identity.FailedInstructionIndex, FailedInstructionName: identity.FailedInstructionName, FailureReason: identity.FailureReason}
					if isExposureEvent(envelope.Type) {
						var exposure ExposureEventContext
						if json.Unmarshal(envelope.Payload, &exposure) != nil || invalidExposureContext(exposure) {
							onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: envelope.Seq, Gap: true})
							continue
						}
						event.Exposure = &exposure
					}
					if isGuiderEvent(envelope.Type) {
						var guider GuiderEventContext
						if err := json.Unmarshal(envelope.Payload, &guider); err != nil || invalidGuiderContext(guider) {
							onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: envelope.Seq, Gap: true})
							continue
						}
						event.Guider = &guider
					}
					if isAutofocusEvent(envelope.Type) {
						var autofocus AutofocusEventContext
						if err := json.Unmarshal(envelope.Payload, &autofocus); err != nil || invalidAutofocusContext(autofocus) {
							onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: envelope.Seq, Gap: true})
							continue
						}
						event.Autofocus = &autofocus
					}
					onEvent(event)
				} else {
					var resume struct {
						Code string `json:"code"`
					}
					if err := json.Unmarshal(payload, &resume); err == nil && resume.Code == "resume_token_expired" {
						onEvent(WebSocketEvent{Type: "ara.resume_gap", Gap: true})
					}
				}
			}
		}
	}
}

func isExposureEvent(eventType string) bool {
	switch eventType {
	case "camera.exposure_started", "camera.exposure_complete", "camera.exposure_failed":
		return true
	default:
		return false
	}
}

func isGuiderEvent(eventType string) bool {
	return eventType == "guider.step" || eventType == "guider.event"
}

func isAutofocusEvent(eventType string) bool { return strings.HasPrefix(eventType, "autofocus.") }

func invalidEventIdentity(timestamp string, payload websocketEventPayload) bool {
	return len(timestamp) > 64 || len(payload.SequenceID) > 256 || len(payload.RunID) > 256 || len(payload.JobID) > 256 || len(payload.FrameID) > 256 || len(payload.DeviceType) > 64 || len(payload.DeviceID) > 256 || len(payload.DeviceName) > 256 || len(payload.Kind) > 64 || len(payload.Details) > 512 || len(payload.Action) > 64 || len(payload.DetectedUTC) > 64 || len(payload.State) > 64 || len(payload.FailedInstructionName) > 256 || len(payload.FailureReason) > 512 || payload.InstructionsCompleted < 0 || payload.InstructionsTotal < 0 || (payload.CurrentInstructionIndex != nil && *payload.CurrentInstructionIndex < 0) || (payload.FailedInstructionIndex != nil && *payload.FailedInstructionIndex < 0)
}

func invalidExposureContext(payload ExposureEventContext) bool {
	return len(payload.FrameID) > 256 || len(payload.Kind) > 64 || len(payload.FilterName) > 128 || len(payload.StartedUTC) > 64 || len(payload.Reason) > 512 ||
		(payload.ExposureSec != nil && (*payload.ExposureSec <= 0 || *payload.ExposureSec > 86400)) ||
		(payload.ElapsedMS != nil && (*payload.ElapsedMS < 0 || *payload.ElapsedMS > 86_400_000))
}

func invalidGuiderContext(payload GuiderEventContext) bool {
	return len(payload.Kind) > 64 || len(payload.Error) > 512 ||
		(payload.Frame != nil && (*payload.Frame < 0 || *payload.Frame > 1_000_000_000)) ||
		invalidFloat(payload.TimeSec, 1_000_000_000) || invalidFloat(payload.RARawPX, 1_000_000_000) ||
		invalidFloat(payload.DecRawPX, 1_000_000_000) || invalidFloat(payload.RAArcsec, 1_000_000_000) ||
		invalidFloat(payload.DecArcsec, 1_000_000_000) || invalidFloat(payload.RADurationMS, 1_000_000_000) ||
		invalidFloat(payload.DecDurationMS, 1_000_000_000) || invalidFloat(payload.PixelScaleArcsec, 1_000_000_000) ||
		invalidFloat(payload.StarMass, 1_000_000_000) || invalidFloat(payload.SNR, 1_000_000_000) ||
		invalidFloat(payload.DXPX, 1_000_000_000) || invalidFloat(payload.DYPX, 1_000_000_000) ||
		invalidFloat(payload.DistancePX, 1_000_000_000) || invalidFloat(payload.SettleTimeSec, 1_000_000_000) ||
		(payload.Status != nil && (*payload.Status < 0 || *payload.Status > 65535))
}

func invalidAutofocusContext(payload AutofocusEventContext) bool {
	if len(payload.Mode) > 32 || len(payload.Phase) > 32 || len(payload.Algorithm) > 64 || len(payload.Severity) > 32 || len(payload.Reason) > 512 {
		return true
	}
	for _, value := range []*int{payload.StepIndex, payload.ShotIndex, payload.Position, payload.Stars, payload.StarsUsed, payload.TotalSteps, payload.FinalPosition, payload.FinalStars, payload.Probes, payload.RestoredPosition} {
		if value != nil && (*value < 0 || *value > 1_000_000_000) {
			return true
		}
	}
	for _, value := range []*float64{payload.HFR, payload.RSquared, payload.BestPosition, payload.PredictedHFR, payload.FinalHFR, payload.DurationSeconds, payload.OffsetPercent, payload.DirectionDegrees} {
		if invalidFloat(value, 1_000_000_000) {
			return true
		}
	}
	return payload.DurationSeconds != nil && *payload.DurationSeconds < 0 ||
		payload.DirectionDegrees != nil && (*payload.DirectionDegrees < 0 || *payload.DirectionDegrees > 360)
}

func invalidFloat(value *float64, limit float64) bool {
	return value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || math.Abs(*value) > limit)
}

func controlWebSocketURL(baseURL string) (string, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint == nil {
		return "", errors.New("ara websocket: invalid base url")
	}
	switch endpoint.Scheme {
	case "http":
		endpoint.Scheme = "ws"
	case "https":
		endpoint.Scheme = "wss"
	default:
		return "", errors.New("ara websocket: base url must use http or https")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/ws"
	endpoint.RawPath = ""
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	return endpoint.String(), nil
}
