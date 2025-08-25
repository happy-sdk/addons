// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package logd

import (
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/logging"
)

type Logger struct {
	mu                sync.RWMutex
	pid               atomic.Int64
	state             *telemetry.DaemonState
	file              *rotatefile.File
	nextRotation      time.Time
	rotationStateFile string
	atel              *AdapterTelemetry
}

func New(state *telemetry.DaemonState) *Logger {
	l := &Logger{
		state: state,
		atel:  &AdapterTelemetry{},
	}
	l.pid.Store(int64(os.Getpid()))
	return l
}

func (l *Logger) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelDebug, ServiceName, pid, msg, args...)
}

func (l *Logger) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelInfo, ServiceName, pid, msg, args...)
}

func (l *Logger) ok(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelOk, ServiceName, pid, msg, args...)
}

func (l *Logger) warn(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelWarn, ServiceName, pid, msg, args...)
}

func (l *Logger) error(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelError, ServiceName, pid, msg, args...)
}
