// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package logd

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/session"
)

type Adapter struct {
	ctx     context.Context
	opts    *logging.Options
	handler slog.Handler
	w       io.WriteCloser
}

type AdapterTelemetry struct {
	Notices        atomic.Uint64
	NotImplemented atomic.Uint64
	Warnings       atomic.Uint64
	Deprecations   atomic.Uint64
	Errors         atomic.Uint64
	Bugs           atomic.Uint64
	Others         atomic.Uint64
	Total          atomic.Uint64
}

func NewAdapter(sess *session.Context, w io.WriteCloser, t *AdapterTelemetry) (logging.Adapter, error) {
	opts, err := sess.Log().Options()
	if err != nil {
		return nil, err
	}

	replaceAttr := opts.ReplaceAttr
	tsfmt := "15:04:05.000"
	if opts.TimestampFormat != "" {
		tsfmt = opts.TimestampFormat
	}

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: opts.LevelVar,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				level := a.Value.Any().(slog.Level)
				lvl := logging.Level(level)
				a.Value = slog.StringValue(lvl.String())
				switch lvl {
				case logging.LevelNotice:
					t.Notices.Add(1)
				case logging.LevelNotImplemented:
					t.NotImplemented.Add(1)
				case logging.LevelWarn:
					t.Warnings.Add(1)
				case logging.LevelDeprecated:
					t.Deprecations.Add(1)
				case logging.LevelError:
					t.Errors.Add(1)
				case logging.LevelBUG:
					t.Bugs.Add(1)
				default:
					t.Others.Add(1)
				}
				t.Total.Add(1)
				return a
			}
			if a.Key == slog.TimeKey {
				// Format the timestamp however you want
				return slog.String(slog.TimeKey, a.Value.Time().Format(tsfmt))
			}
			if replaceAttr != nil {
				a = replaceAttr(groups, a)
			}
			return a
		},
		AddSource: opts.AddSource,
	})
	return &Adapter{
		ctx:     sess.Context(),
		opts:    opts,
		handler: handler,
		w:       w,
	}, nil
}

func (a *Adapter) Options() *logging.Options {
	return a.opts
}

func (a *Adapter) Context() context.Context {
	return a.ctx
}

func (a *Adapter) Dispose() error {
	// keep adapter unlocked, to enable last logs to be written
	time.Sleep(time.Millisecond * 10)
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])

	a.handler.Handle(
		a.ctx,
		slog.NewRecord(time.Now(),
			slog.LevelDebug,
			"daemon logfile closed",
			pcs[0],
		),
	)

	return a.w.Close()
}

func (a *Adapter) Handler() slog.Handler {
	return a.handler
}
