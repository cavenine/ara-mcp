// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"net/http"
)

// ServerInfo is Ara's server identity and API version response.
type ServerInfo struct {
	UUID     string `json:"server_uuid"`
	Nickname string `json:"nickname"`
	Version  string `json:"version"`
	API      string `json:"api"`
	Tier     string `json:"tier"`
}

// ServerVersions describes the daemon build and versioned API surfaces.
type ServerVersions struct {
	DaemonVersion string       `json:"daemon_version"`
	DaemonGitSHA  string       `json:"daemon_git_sha"`
	DotnetVersion string       `json:"dotnet_version"`
	APISurfaces   []APISurface `json:"api_surfaces"`
}

// APISurface is one versioned Ara API surface.
type APISurface struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ServerSession reports the current Ara handoff-session state without its capability.
type ServerSession struct {
	Connected   bool     `json:"connected"`
	Hostname    string   `json:"hostname"`
	IdleSeconds *float64 `json:"idle_seconds"`
}

// DeviceType selects a fixed Ara equipment-status endpoint.
type DeviceType string

const (
	// DeviceCamera selects Ara's camera status endpoint.
	DeviceCamera DeviceType = "camera"
	// DeviceTelescope selects Ara's telescope status endpoint.
	DeviceTelescope DeviceType = "telescope"
	// DeviceFocuser selects Ara's focuser status endpoint.
	DeviceFocuser DeviceType = "focuser"
	// DeviceFilterWheel selects Ara's filter-wheel status endpoint.
	DeviceFilterWheel DeviceType = "filterwheel"
	// DeviceGuider selects Ara's guider status endpoint.
	DeviceGuider DeviceType = "guider"
)

// CheckServerWithRequestID checks Ara's server-info endpoint without decoding its body.
func (c *Client) CheckServerWithRequestID(ctx context.Context, requestID string) (Result, error) {
	return c.do(ctx, request{Method: http.MethodGet, Route: "/server/info", RequestID: requestID}, nil)
}

// GetServerInfoWithRequestID reads Ara's server identity.
func (c *Client) GetServerInfoWithRequestID(ctx context.Context, requestID string) (ServerInfo, Result, error) {
	var response ServerInfo
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/server/info", RequestID: requestID}, &response)
	return response, result, err
}

// GetServerVersionsWithRequestID reads Ara's daemon and API surface versions.
func (c *Client) GetServerVersionsWithRequestID(ctx context.Context, requestID string) (ServerVersions, Result, error) {
	var response ServerVersions
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/server/versions", RequestID: requestID}, &response)
	return response, result, err
}

// GetServerStateWithRequestID reads the opaque point-in-time server state.
func (c *Client) GetServerStateWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/server/state", requestID)
}

// GetServerSessionWithRequestID reads the current handoff-session status.
func (c *Client) GetServerSessionWithRequestID(ctx context.Context, requestID string) (ServerSession, Result, error) {
	var response ServerSession
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/server/session", RequestID: requestID}, &response)
	return response, result, err
}

// GetProfileSiteWithRequestID reads the active profile's site data.
func (c *Client) GetProfileSiteWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/profile/site", requestID)
}

// GetProfileImagingDefaultsWithRequestID reads the active profile's imaging defaults.
func (c *Client) GetProfileImagingDefaultsWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/profile/imaging-defaults", requestID)
}

// GetProfileStorageWithRequestID reads the selected profile's capture storage settings.
func (c *Client) GetProfileStorageWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/profile/storage", requestID)
}

// GetProfileFilterWheelLabelsWithRequestID reads configured filter-wheel labels.
func (c *Client) GetProfileFilterWheelLabelsWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/profile/filter-wheel/labels", requestID)
}

// GetProfileFilterSetWithRequestID reads the active profile's filter set.
func (c *Client) GetProfileFilterSetWithRequestID(ctx context.Context, requestID string) (jsontext.Value, Result, error) {
	return c.getJSONWithRequestID(ctx, "/profile/filter-set", requestID)
}

// GetDeviceStatusWithRequestID reads status for one supported Ara equipment type.
func (c *Client) GetDeviceStatusWithRequestID(ctx context.Context, device DeviceType, requestID string) (jsontext.Value, Result, error) {
	switch device {
	case DeviceCamera, DeviceTelescope, DeviceFocuser, DeviceFilterWheel, DeviceGuider:
	default:
		return nil, Result{Outcome: OutcomeFailed}, errors.New("ara request: unsupported equipment type")
	}
	return c.getJSONWithRequestID(ctx, "/equipment/"+string(device), requestID)
}

func (c *Client) getJSONWithRequestID(ctx context.Context, route, requestID string) (jsontext.Value, Result, error) {
	var response jsontext.Value
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: route, RequestID: requestID}, &response)
	return response, result, err
}
