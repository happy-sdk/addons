// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/happy-sdk/happy/pkg/version"
	"github.com/happy-sdk/happy/sdk/session"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

var (
	Error = errors.New("telemetry")
)

type Setup func(sess *session.Context, res *resource.Resource) (p metric.MeterProvider, err error)

// Telemetry holds global telemetry collection.
type Telemetry struct {
	mu       sync.RWMutex
	disabled atomic.Bool
	setup    Setup
	provider metric.MeterProvider

	process Process
	logger  Logger
}

// Telemetry initializes a new DaemonState.
func New() *Telemetry {
	return &Telemetry{}
}

func (t *Telemetry) Setup(setup Setup) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.setup != nil {
		return
	}
	t.setup = setup
}

func (t *Telemetry) Start(sess *session.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.disabled.Load() || t.setup == nil {
		sess.Log().Debug("telemetry disabled no provider")
		t.disabled.Store(true)
		return nil
	}

	res := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(sess.Get("app.name").String()),
		semconv.ServiceVersion(sess.Get("app.version").String()),
		semconv.ServiceNamespace(sess.Get("app.slug").String()),
		semconv.ServiceInstanceID(sess.Get("app.instance.id").String()),
		semconv.DeploymentEnvironment(sess.Get("app.profile.name").String()),
	)

	var ver version.Version
	bi, _ := debug.ReadBuildInfo()
	for _, dep := range bi.Deps {
		if dep.Path != "github.com/happy-sdk/addons/daemon" {
			continue
		}
		var err error
		ver, err = version.Parse(dep.Version)
		if err != nil {
			ver = "v0.0.1"
		}
	}

	provider, err := t.setup(sess, res)
	if err != nil {
		return err
	}

	if err := t.process.otelConfigure(sess.Context(), t,
		provider.Meter(
			t.process.Service.Slug,
			metric.WithSchemaURL(semconv.SchemaURL),
			metric.WithInstrumentationVersion(ver.String()),
		)); err != nil {
		return err
	}
	if err := t.logger.otelConfigure(sess.Context(), t,
		provider.Meter(
			t.logger.Service.Slug,
			metric.WithSchemaURL(semconv.SchemaURL),
			metric.WithInstrumentationVersion(ver.String()),
		)); err != nil {
		return err
	}
	t.provider = provider
	t.setup = nil
	return nil
}

func (t *Telemetry) Stop(sess *session.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.disabled.Load() || t.provider == nil {
		return nil
	}
	if shutdownable, ok := t.provider.(interface{ Shutdown(context.Context) error }); ok {
		if err := shutdownable.Shutdown(context.Background()); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot returns the current state as a Snapshot.
func (t *Telemetry) Snapshot(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	if !t.disabled.Load() && t.provider != nil {
		if err := t.process.otelUpdate(ctx); err != nil {
			return Snapshot{}, err
		}
		if err := t.logger.otelUpdate(ctx); err != nil {
			return Snapshot{}, err
		}
	}

	snapshot := Snapshot{
		Process:   t.process,
		Logger:    t.logger,
		Timestamp: time.Now(),
	}

	return snapshot, nil
}

type otelMetricConfig struct {
	name                      string
	description               string
	unit                      string
	int64CounterPtr           *metric.Int64Counter
	int64GaugePtr             *metric.Int64Gauge
	int64ObservableCounterPtr *metric.Int64ObservableCounter
	int64ObservableGaugePtr   *metric.Int64ObservableGauge
	int64Histogram            *metric.Int64Histogram
	float64ObservableGaugePtr *metric.Float64ObservableGauge
	millisecondsHistogramPtr  *metric.Float64Histogram
}

func (t *Telemetry) otelConfigure(meter metric.Meter, cnfs []otelMetricConfig) (instruments []metric.Observable, err error) {
	for _, cfg := range cnfs {

		if cfg.int64GaugePtr != nil {
			inst, err := meter.Int64Gauge(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
			)
			if err != nil {
				return nil, err
			}
			*cfg.int64GaugePtr = inst
		} else if cfg.int64CounterPtr != nil {
			inst, err := meter.Int64Counter(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
			)
			if err != nil {
				return nil, err
			}
			*cfg.int64CounterPtr = inst
		} else if cfg.int64ObservableCounterPtr != nil {
			inst, err := meter.Int64ObservableCounter(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
			)
			if err != nil {
				return nil, err
			}
			*cfg.int64ObservableCounterPtr = inst
			instruments = append(instruments, inst)
		} else if cfg.int64ObservableGaugePtr != nil {
			inst, err := meter.Int64ObservableGauge(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
			)
			if err != nil {
				return nil, err
			}
			*cfg.int64ObservableGaugePtr = inst
			instruments = append(instruments, inst)
		} else if cfg.float64ObservableGaugePtr != nil {
			inst, err := meter.Float64ObservableGauge(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
			)
			if err != nil {
				return nil, err
			}
			*cfg.float64ObservableGaugePtr = inst
			instruments = append(instruments, inst)
		} else if cfg.millisecondsHistogramPtr != nil {
			histogram, err := meter.Float64Histogram(
				cfg.name,
				metric.WithDescription(cfg.description),
				metric.WithUnit(cfg.unit),
				metric.WithExplicitBucketBoundaries(
					0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, 7500, 10000,
				),
			)
			if err != nil {
				return nil, err
			}
			*cfg.millisecondsHistogramPtr = histogram
		} else {
			return nil, fmt.Errorf("otel instument ptr implementation missing", Error)
		}
	}
	return instruments, nil
}
