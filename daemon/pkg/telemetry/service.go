// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type ServiceStatus int8

const (
	ServiceStatusUnknown ServiceStatus = iota
	ServiceStatusDisabled
	ServiceStatusIdle
	ServiceStatusStarting
	ServiceStatusRestarting
	ServiceStatusRunning
	ServiceStatusStopping
	ServiceStatusStopped
	ServiceStatusFailed
)

type Service struct {
	Name            string
	Slug            string
	Addr            string
	Errors          map[time.Time]error
	StartedAt       time.Time
	StartUpTook     time.Duration
	prevStartUpTook time.Duration
	StoppedAt       time.Time
	Status          ServiceStatus

	info            metric.Int64ObservableGauge
	errors          metric.Int64Counter
	uptime          metric.Int64ObservableGauge
	startupDuration metric.Float64Histogram
	status          metric.Int64ObservableGauge
	stoppedAt       metric.Int64ObservableGauge
}

func (s *Service) otelConfig(prefix string) []otelMetricConfig {
	return []otelMetricConfig{
		{
			name:                    prefix + ".info",
			description:             "Service metadata",
			unit:                    "1",
			int64ObservableGaugePtr: &s.info,
		},
		{
			name:            prefix + ".errors",
			description:     "Count of service errors",
			unit:            "count",
			int64CounterPtr: &s.errors,
		},
		{
			name:                    prefix + ".uptime",
			description:             "Service uptime since start",
			unit:                    "seconds",
			int64ObservableGaugePtr: &s.uptime,
		},
		{
			name:                     prefix + ".startup_duration",
			description:              "Service startup duration",
			unit:                     "milliseconds",
			millisecondsHistogramPtr: &s.startupDuration,
		},
		{
			name:                    prefix + ".status",
			description:             "Service status (0=unknown, 1=disabled, 2=idle, 3=starting, 4=restarting, 5=running, 6=stopping, 7=stopped, 8=failed)",
			unit:                    "1",
			int64ObservableGaugePtr: &s.status,
		},
		{
			name:                    prefix + ".stopped_at",
			description:             "Timestamp when service stopped",
			unit:                    "seconds",
			int64ObservableGaugePtr: &s.stoppedAt,
		},
	}
}

func (s *Service) getCommonAttrs() metric.MeasurementOption {
	return metric.WithAttributeSet(attribute.NewSet(
		attribute.String("svc_slug", s.Slug),
		attribute.String("svc_name", s.Name),
		attribute.String("svc_addr", s.Addr),
	))
}

func (s *Service) otelUpdate(ctx context.Context) {
	if s.Slug == "" {
		return
	}

	opts := s.getCommonAttrs()

	var setErr bool
	if s.startupDuration != nil && s.StartUpTook > 0 && s.prevStartUpTook != s.StartUpTook {
		took := float64(s.StartUpTook) / float64(time.Millisecond)
		s.prevStartUpTook = s.StartUpTook

		s.startupDuration.Record(ctx, took, opts)
		setErr = true
	}

	if s.errors != nil || setErr {
		errc := len(s.Errors)
		s.errors.Add(ctx, int64(errc), opts)
	}
}

func (s *Service) otelObserve(o metric.Observer) {
	if s.Slug == "" {
		return
	}
	opts := s.getCommonAttrs()

	o.ObserveInt64(s.info, 1, opts)
	o.ObserveInt64(s.uptime, int64(time.Since(s.StartedAt).Seconds()), opts)
	o.ObserveInt64(s.status, int64(s.Status), opts)
	stoppedAt := int64(0)
	if !s.StoppedAt.IsZero() {
		stoppedAt = s.StoppedAt.Unix()
	}
	o.ObserveInt64(s.stoppedAt, stoppedAt, opts)
}
