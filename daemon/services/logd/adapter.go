// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package logd

import (
	"context"
	"io"
	"log/slog"
	"math"
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
	LevelHappy          atomic.Uint64
	LevelHappyInit      atomic.Uint64
	LevelDebug          atomic.Uint64
	LevelInfo           atomic.Uint64
	LevelOk             atomic.Uint64
	LevelNotice         atomic.Uint64
	LevelNotImplemented atomic.Uint64
	LevelWarn           atomic.Uint64
	LevelDeprecated     atomic.Uint64
	LevelError          atomic.Uint64
	LevelBUG            atomic.Uint64
	LevelAlways         atomic.Uint64
	Total               atomic.Uint64
}

func NewAdapter(sess *session.Context, w io.WriteCloser, atel *AdapterTelemetry) (logging.Adapter, error) {
	opts, err := sess.Log().Options()
	if err != nil {
		return nil, err
	}

	replaceAttr := opts.ReplaceAttr
	tsfmt := "15:04:05.000"
	if opts.TimestampFormat != "" {
		tsfmt = opts.TimestampFormat
	}

	levelHappy := logging.Level(math.MinInt)
	levelInit := logging.Level(levelHappy + 1)

	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: opts.LevelVar,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.LevelKey {
				level := a.Value.Any().(slog.Level)
				lvl := logging.Level(level)
				a.Value = slog.StringValue(lvl.String())
				switch lvl {
				case levelHappy:
					atel.LevelHappy.Add(1)
				case levelInit:
					atel.LevelHappyInit.Add(1)
				case logging.LevelDebug:
					atel.LevelDebug.Add(1)
				case logging.LevelInfo:
					atel.LevelInfo.Add(1)
				case logging.LevelOk:
					atel.LevelOk.Add(1)
				case logging.LevelNotice:
					atel.LevelNotice.Add(1)
				case logging.LevelNotImplemented:
					atel.LevelNotImplemented.Add(1)
				case logging.LevelWarn:
					atel.LevelWarn.Add(1)
				case logging.LevelDeprecated:
					atel.LevelDeprecated.Add(1)
				case logging.LevelError:
					atel.LevelError.Add(1)
				case logging.LevelBUG:
					atel.LevelBUG.Add(1)
				default:
					atel.LevelAlways.Add(1)
				}
				atel.Total.Add(1)
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
