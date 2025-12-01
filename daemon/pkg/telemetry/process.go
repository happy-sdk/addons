// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/metric"
)

// Process holds process-manager state and telemetry.
type Process struct {
	PID       int64
	pid       metric.Int64Gauge
	UpdatedAt time.Time
	Service   Service
	Busy      bool
}

// UpdateProcess applies f to the process-manager state under write lock.
func (t *Telemetry) UpdateProcess(f func(*Process)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	f(&t.process)
	t.process.UpdatedAt = time.Now()

	if t.disabled.Load() {
		return
	}
	t.logger.Service.otelUpdate(context.Background())
}

// ProcessManager returns a copy of the state under read lock.
func (t *Telemetry) Process() Process {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.process
}

func (proc *Process) AddError(err error) {
	if proc.Service.Errors == nil {
		proc.Service.Errors = make(map[time.Time]error)
	}
	proc.Service.Errors[time.Now()] = err
}

func (proc *Process) otelConfigure(ctx context.Context, t *Telemetry, meter metric.Meter) error {
	var err error

	metricsCnf := []otelMetricConfig{
		{
			name:          "process.pid",
			description:   "Daemon Process ID",
			int64GaugePtr: &proc.pid,
		},
	}

	metricsCnf = append(metricsCnf, proc.Service.otelConfig("daemon.service")...)

	instruments, err := t.otelConfigure(meter, metricsCnf)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		t.mu.RLock()
		defer t.mu.RUnlock()
		proc.Service.otelObserve(o)
		return nil
	}, instruments...)
	if err != nil {
		return err
	}
	proc.Service.otelUpdate(ctx)

	return proc.otelUpdate(ctx)
}

func (proc *Process) otelUpdate(ctx context.Context) error {
	if proc.Service.Slug == "" {
		return nil
	}
	proc.pid.Record(ctx, proc.PID)
	proc.Service.otelUpdate(ctx)
	return nil
}
