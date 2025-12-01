// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

type Instance struct {
	pid atomic.Int64
}

func New() *Instance {
	inst := &Instance{}
	inst.pid.Store(int64(os.Getpid()))
	return inst
}

func (inst *Instance) Service() *services.Service {
	svc := services.New(service.Config{
		Name:          "daemon-ipc",
		Description:   "Daemon control service",
		RetryOnError:  false,
		MaxRetries:    0,
		RetryBackoff:  settings.Duration(5 * time.Second),
		LoaderTimeout: settings.Duration(time.Second * 5),
	})

	svc.OnRegister(func(sess *session.Context) error {
		ipcSocketPath := fmt.Sprintf("@%s-daemon.sock", sess.Get("app.slug").String())
		if err := sess.Opts().Set("daemon.ipc.socket", ipcSocketPath); err != nil {
			return fmt.Errorf("failed to set daemon ctl socket path: %w", err)
		}

		inst.debug(sess.Log(), "daemon-ipc service registered")
		return nil
	})

	svc.OnStart(func(sess *session.Context) error {
		sess.Log().Log(sess.Context(), logging.LevelNotImpl.Level(), "daemon-ipc.OnStart started")
		return nil
	})

	svc.OnStop(func(sess *session.Context, err error) error {
		if err != nil {
			sess.Log().Error("daemon-ipc.OnStop")
			sess.Log().Error(err.Error())
			return nil
		}
		sess.Log().Log(sess.Context(), logging.LevelNotImpl.Level(), "daemon-ipc.OnStop")
		return nil
	})

	return svc
}

func (inst *Instance) debug(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	logd.DaemonLog(logger, logging.LevelDebug, ServiceName, pid, msg, args...)
}

func (inst *Instance) info(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	logd.DaemonLog(logger, logging.LevelInfo, ServiceName, pid, msg, args...)
}
