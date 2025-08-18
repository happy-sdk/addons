// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"log/slog"
	"time"

	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

func (inst *Instance) workerService() *services.Service {
	svc := services.New(service.Config{
		Name:         "daemon-worker",
		Description:  "Daemon service",
		RetryOnError: true,
		MaxRetries:   3,
		RetryBackoff: settings.Duration(5 * time.Second),
	})

	svc.OnStart(func(sess *session.Context) error {
		if inst.running.Load() {
			return ErrAlreadyRunning
		}
		// Hold the lock to prevent concurrent start
		inst.mu.Lock()
		defer inst.mu.Unlock()
		sess.Dispatch(StartingEvent)
		inst.workerDebug(sess.Log(), "starting...")

		if err := inst.startAction(sess, inst.api.args); err != nil {
			return err
		}
		inst.running.Store(true)
		inst.workerOk(sess.Log(), "started")
		sess.Dispatch(StartedEvent)
		return nil
	})

	svc.OnStop(func(sess *session.Context, err error) error {
		if !inst.running.Load() {
			return ErrNotRunning
		}
		// Hold the lock to prevent concurrent stop
		inst.mu.Lock()
		defer inst.mu.Unlock()
		sess.Dispatch(StoppingEvent)
		inst.workerDebug(sess.Log(), "stopping...")

		if err := inst.stopAction(sess, err); err != nil {
			return err
		}
		inst.workerOk(sess.Log(), "stopped")
		inst.running.Store(false)

		sess.Dispatch(StoppedEvent)
		return nil
	})

	svc.Cron(inst.cronSetup)

	return svc
}

func (inst *Instance) workerDebug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelDebug, "daemon-worker", pid, msg, args...)
}

func (inst *Instance) workerOk(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelOk, "daemon-worker", pid, msg, args...)
}
