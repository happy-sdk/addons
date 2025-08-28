// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors
package ctl

import (
	"log/slog"
	"os"
	"sync/atomic"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/logging"
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
		Name:         "daemon-ctl",
		Description:  "Daemon control service",
		RetryOnError: false,
		MaxRetries:   0,
	})

	svc.OnRegister(func(sess *session.Context) error {
		inst.debug(sess.Log(), "daemon-ctl service registered")
		return nil
	})

	return svc
}

func (inst *Instance) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	logd.DaemonLog(logger, logging.LevelDebug, ServiceName, pid, msg, args...)
}

func (inst *Instance) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	logd.DaemonLog(logger, logging.LevelInfo, ServiceName, pid, msg, args...)
}
