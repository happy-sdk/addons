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
	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/sdk/session"
)

type Logger struct {
	mu    sync.RWMutex
	pid   atomic.Int64
	state *telemetry.DaemonState
	file  *rotatefile.File
	atel  *AdapterTelemetry
}

func New(state *telemetry.DaemonState) *Logger {
	l := &Logger{
		state: state,
		atel:  &AdapterTelemetry{},
	}
	l.pid.Store(int64(os.Getpid()))
	return l
}

func (l *Logger) statsUpdateLoggerFsStats(sess *session.Context) {
	l.mu.Lock()
	defer l.mu.Unlock()

	logdirPath := sess.Settings().Get("daemon.fs.path.logs").String()
	dirsize, err := fsutils.DirSize(logdirPath)
	if err != nil {
		l.warn(sess.Log(), "failed to get log dir size", slog.String("err", err.Error()))
		return
	}
	l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.DirSize = bytesize.SISize(dirsize)
	})
}

func (l *Logger) statsUpdateNextRotation(sess *session.Context) error {
	rotationScheduleExpr := sess.Settings().Get("daemon.log.rotation_schedule").String()
	rotationSchedule, err := cron.ParseWithOptionalSecond(rotationScheduleExpr)
	if err != nil {
		return err
	}
	nextRotation := rotationSchedule.Next(time.Now())

	l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.NextRotation = nextRotation
	})
	return nil
}

func (l *Logger) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelDebug, ServiceName, pid, msg, args...)
}

func (l *Logger) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := l.pid.Load()
	DaemonLog(logger, logging.LevelInfo, ServiceName, pid, msg, args...)
}

func (l *Logger) notice(logger logging.Logger, msg string, args ...slog.Attr) {
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
