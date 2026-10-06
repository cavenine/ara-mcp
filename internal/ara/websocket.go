// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const araWebSocketVersion = "1"

// WebSocketEvent is the bounded identity/progress subset consumed from Ara's event stream.
type WebSocketEvent struct {
	Type                    string `json:"type"`
	Seq                     int64  `json:"seq"`
	Gap                     bool   `json:"gap,omitzero"`
	SequenceID              string `json:"sequence_id,omitempty"`
	RunID                   string `json:"run_id,omitempty"`
	JobID                   string `json:"job_id,omitempty"`
	FrameID                 string `json:"frame_id,omitempty"`
	State                   string `json:"state,omitempty"`
	InstructionsCompleted   int    `json:"instructions_completed"`
	InstructionsTotal       int    `json:"instructions_total"`
	CurrentInstructionIndex *int   `json:"current_instruction_index,omitempty"`
	FailedInstructionIndex  *int   `json:"failed_instruction_index,omitempty"`
	FailedInstructionName   string `json:"failed_instruction_name,omitempty"`
	FailureReason           string `json:"failure_reason,omitempty"`
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
				var envelope struct {
					Type    string `json:"type"`
					Seq     int64  `json:"seq"`
					Payload struct {
						SequenceID              string `json:"sequence_id"`
						RunID                   string `json:"run_id"`
						JobID                   string `json:"job_id"`
						FrameID                 string `json:"frame_id"`
						State                   string `json:"state"`
						InstructionsCompleted   int    `json:"instructions_completed"`
						InstructionsTotal       int    `json:"instructions_total"`
						CurrentInstructionIndex *int   `json:"current_instruction_index"`
						FailedInstructionIndex  *int   `json:"failed_instruction_index"`
						FailedInstructionName   string `json:"failed_instruction_name"`
						FailureReason           string `json:"failure_reason"`
					} `json:"payload"`
				}
				if err := json.Unmarshal(payload, &envelope); err == nil && len(envelope.Type) <= 128 && envelope.Type != "" && envelope.Seq > 0 {
					identity := envelope.Payload
					if len(identity.SequenceID) > 256 || len(identity.RunID) > 256 || len(identity.JobID) > 256 || len(identity.FrameID) > 256 || len(identity.State) > 64 || len(identity.FailedInstructionName) > 256 || len(identity.FailureReason) > 512 || identity.InstructionsCompleted < 0 || identity.InstructionsTotal < 0 || (identity.CurrentInstructionIndex != nil && *identity.CurrentInstructionIndex < 0) || (identity.FailedInstructionIndex != nil && *identity.FailedInstructionIndex < 0) {
						onEvent(WebSocketEvent{Type: "ara.event_gap", Seq: envelope.Seq, Gap: true})
						continue
					}
					onEvent(WebSocketEvent{Type: envelope.Type, Seq: envelope.Seq, SequenceID: identity.SequenceID, RunID: identity.RunID, JobID: identity.JobID, FrameID: identity.FrameID, State: identity.State, InstructionsCompleted: identity.InstructionsCompleted, InstructionsTotal: identity.InstructionsTotal, CurrentInstructionIndex: identity.CurrentInstructionIndex, FailedInstructionIndex: identity.FailedInstructionIndex, FailedInstructionName: identity.FailedInstructionName, FailureReason: identity.FailureReason})
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
