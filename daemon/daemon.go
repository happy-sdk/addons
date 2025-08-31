// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"
)

var (
	Error    = errors.New("daemon")
	ErrSetup = fmt.Errorf("%w setup", Error)
	ErrPath  = fmt.Errorf("%w path", Error)
)
