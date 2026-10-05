// Copyright (c) 2026 OpenAstro Contributors
// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package monitor

import "errors"

func readProcessStats() (processStats, error) {
	return processStats{}, errors.New("process CPU and RSS are unavailable on this platform")
}
