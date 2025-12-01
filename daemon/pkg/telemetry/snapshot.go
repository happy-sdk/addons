// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"time"
)

// Snapshot captures the current telemetry state.
type Snapshot struct {
	Process   Process
	Logger    Logger
	Timestamp time.Time
}
