// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const araWebSocketVersion = "1"

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
	dialContext, cancel := context.WithTimeout(ctx, c.gateway.timeout)
	defer cancel()
	header := make(http.Header)
	header.Set("X-Ara-Session", session.sessionID)
	header.Set("X-Ara-WS-Version", araWebSocketVersion)
	connection, response, err := websocket.Dial(dialContext, endpoint, &websocket.DialOptions{
		HTTPHeader: header,
		HTTPClient: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	})
	if err == nil {
		return connection, nil
	}
	if dialContext.Err() != nil {
		return nil, dialContext.Err()
	}
	if response != nil {
		return nil, fmt.Errorf("ara websocket: handshake failed with http status %d", response.StatusCode)
	}
	return nil, errors.New("ara websocket: dial failed")
}

// MaintainControlWebSocket reads one session-bound socket, answers Ara's
// application-level heartbeat, and rejects takeover requests so a human holder
// is not displaced. Other event frames are reserved for the T08 event consumer.
func (c *Client) MaintainControlWebSocket(ctx context.Context, conn *websocket.Conn, onHeartbeat func(time.Time), onTakeoverRejected func()) error {
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
