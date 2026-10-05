// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

package ara

import (
	"context"
	"net/http"
)

// ProfileList is Ara's profile-library index and selected profile ID.
type ProfileList struct {
	ActiveID *string        `json:"active_id"`
	Profiles []ProfileBrief `json:"profiles"`
}

// ProfileBrief identifies a saved Ara profile without exposing its settings.
type ProfileBrief struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListProfilesWithRequestID reads Ara's profile-library selection. Unlike the
// server-state snapshot on the pinned scaffold build, active_id reflects the
// profile selected through the Profiles API.
func (c *Client) ListProfilesWithRequestID(ctx context.Context, requestID string) (ProfileList, Result, error) {
	var response ProfileList
	result, err := c.do(ctx, request{Method: http.MethodGet, Route: "/profiles", RequestID: requestID}, &response)
	return response, result, err
}
