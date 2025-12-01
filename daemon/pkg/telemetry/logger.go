// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"context"
	"time"

	"github.com/happy-sdk/happy/pkg/bytesize"
	"go.opentelemetry.io/otel/metric"
)

type Logger struct {
	UpdatedAt     time.Time
	Service       Service
	NextRotation  time.Time
	Rotations     int64
	rotations     metric.Int64ObservableCounter
	DirSize       bytesize.SISize
	dirSize       metric.Int64ObservableGauge
	BackupDirSize bytesize.SISize
	backupDirSize metric.Int64ObservableGauge

	LevelHappy     uint64
	levelHappy     metric.Int64ObservableCounter
	LevelHappyInit uint64
	levelHappyInit metric.Int64ObservableCounter
	LevelDebug     uint64
	levelDebug     metric.Int64ObservableCounter
	LevelInfo      uint64
	levelInfo      metric.Int64ObservableCounter
	LevelOk        uint64
	levelOk        metric.Int64ObservableCounter
	LevelNotice    uint64
	levelNotice    metric.Int64ObservableCounter
	LevelNotImpl   uint64
	levelNotImpl   metric.Int64ObservableCounter
	LevelWarn      uint64
	levelWarn      metric.Int64ObservableCounter
	LevelDepr      uint64
	levelDepr      metric.Int64ObservableCounter
	LevelError     uint64
	levelError     metric.Int64ObservableCounter
	LevelBUG       uint64
	levelBUG       metric.Int64ObservableCounter
	LevelOut       uint64
	levelOut       metric.Int64ObservableCounter
	Total          uint64
	total          metric.Int64ObservableCounter
}

// UpdateLogger applies f to the Logger state under write lock.
func (t *Telemetry) UpdateLogger(f func(*Logger)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f(&t.logger)
	t.logger.UpdatedAt = time.Now()

	if t.disabled.Load() {
		return
	}
	t.logger.otelUpdate(context.Background())
}

// Logger returns a copy of the Logger state under read lock.
func (s *Telemetry) Logger() Logger {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logger
}

func (l *Logger) AddError(err error) {
	if l.Service.Errors == nil {
		l.Service.Errors = make(map[time.Time]error)
	}
	l.Service.Errors[time.Now()] = err
}

func (l *Logger) otelConfigure(ctx context.Context, t *Telemetry, meter metric.Meter) error {
	metricsCnf := []otelMetricConfig{
		{
			name:                      "logger.rotations",
			description:               "Log file rotations since daemon startup",
			unit:                      "count",
			int64ObservableCounterPtr: &l.rotations,
		},
		{
			name:                      "logger.level.happy",
			description:               "Count of log entries for level (happy)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelHappy,
		},
		{
			name:                      "logger.level.happy_init",
			description:               "Count of log entries for level (happy_init)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelHappyInit,
		},
		{
			name:                      "logger.level.debug",
			description:               "Count of log entries for level (debug)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelDebug,
		},
		{
			name:                      "logger.level.info",
			description:               "Count of log entries for level (info)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelInfo,
		},
		{
			name:                      "logger.level.ok",
			description:               "Count of log entries for level (ok)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelOk,
		},
		{
			name:                      "logger.level.notice",
			description:               "Count of log entries for level (notice)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelNotice,
		},
		{
			name:                      "logger.level.notimplemented",
			description:               "Count of log entries for level (levelNotImplemented)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelNotImpl,
		},
		{
			name:                      "logger.level.warn",
			description:               "Count of log entries for level (warn)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelWarn,
		},
		{
			name:                      "logger.level.deprecated",
			description:               "Count of log entries for level (deprecated)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelDepr,
		},
		{
			name:                      "logger.level.error",
			description:               "Count of log entries for level (error)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelError,
		},
		{
			name:                      "logger.level.bug",
			description:               "Count of log entries for level (bug)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelBUG,
		},
		{
			name:                      "logger.level.out",
			description:               "Count of log entries for level (always)",
			unit:                      "count",
			int64ObservableCounterPtr: &l.levelOut,
		},
		{
			name:                      "logger.entries.total",
			description:               "Count of log entries for all levels",
			unit:                      "count",
			int64ObservableCounterPtr: &l.total,
		},
		{
			name:                    "logger.dir_size",
			description:             "Size of log directory",
			unit:                    "bytes",
			int64ObservableGaugePtr: &l.dirSize,
		},
		{
			name:                    "logger.backup_dir_size",
			description:             "Size of backup log directory",
			unit:                    "bytes",
			int64ObservableGaugePtr: &l.backupDirSize,
		},
	}
	metricsCnf = append(metricsCnf, l.Service.otelConfig("logger.service")...)

	instruments, err := t.otelConfigure(meter, metricsCnf)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		t.mu.RLock()
		defer t.mu.RUnlock()
		o.ObserveInt64(l.levelHappy, int64(l.LevelHappy))
		o.ObserveInt64(l.levelHappyInit, int64(l.LevelHappyInit))
		o.ObserveInt64(l.levelDebug, int64(l.LevelDebug))
		o.ObserveInt64(l.levelInfo, int64(l.LevelInfo))
		o.ObserveInt64(l.levelOk, int64(l.LevelOk))
		o.ObserveInt64(l.levelNotice, int64(l.LevelNotice))
		o.ObserveInt64(l.levelNotImpl, int64(l.LevelNotImpl))
		o.ObserveInt64(l.levelWarn, int64(l.LevelWarn))
		o.ObserveInt64(l.levelDepr, int64(l.LevelDepr))
		o.ObserveInt64(l.levelError, int64(l.LevelError))
		o.ObserveInt64(l.levelBUG, int64(l.LevelBUG))
		o.ObserveInt64(l.levelOut, int64(l.LevelOut))

		o.ObserveInt64(l.rotations, int64(l.Rotations))
		o.ObserveInt64(l.total, int64(l.Total))
		o.ObserveInt64(l.dirSize, int64(l.DirSize.Bytes()))
		o.ObserveInt64(l.backupDirSize, int64(l.BackupDirSize.Bytes()))

		l.Service.otelObserve(o)
		return nil
	}, instruments...)
	if err != nil {
		return err
	}

	l.Service.otelUpdate(ctx)

	return nil
}

func (l *Logger) otelUpdate(ctx context.Context) error {
	if l.Service.Slug == "" {
		return nil
	}
	l.Service.otelUpdate(ctx)
	return nil
}
