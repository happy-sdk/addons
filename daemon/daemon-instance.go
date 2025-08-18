// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"os"
	"sync"
	"sync/atomic"

	"github.com/happy-sdk/addons/daemon/healthcheck"
	"github.com/happy-sdk/addons/daemon/ipc"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/session"
)

type Instance struct {
	mu      sync.RWMutex
	pid     atomic.Int64
	running atomic.Bool
	busy    atomic.Bool

	startAction action.WithArgs
	stopAction  action.WithPrevErr
	cronSetup   func(schedule services.CronScheduler)
	status      *healthcheck.Status
	api         *API
}

func newInstance(s *Setup, api *API) *Instance {
	inst := &Instance{
		api:       api,
		cronSetup: s.cronSetup,
		status:    s.status,
	}
	inst.pid.Store(int64(os.Getpid()))

	inst.startActionStub(s)
	inst.stopActionStub(s)

	return inst
}

// IsRunning returns true if the daemon is running.
func (inst *Instance) IsRunning() bool {
	return inst.running.Load()
}

func (inst *Instance) startActionStub(s *Setup) {
	if s.startAction != nil {
		inst.startAction = s.startAction
		return
	}

	inst.startAction = func(sess *session.Context, args action.Args) error {
		inst.workerDebug(sess.Log(), "skipping: no start action")
		return nil
	}
}

func (inst *Instance) stopActionStub(s *Setup) {
	if s.stopAction != nil {
		inst.stopAction = s.stopAction
		return
	}
	inst.stopAction = func(sess *session.Context, prevErr error) error {
		inst.workerDebug(sess.Log(), "skipping: no stop action")
		return nil
	}
}

func (d *Instance) ipcService() *services.Service {
	ipcsvc := ipc.NewService()
	return ipcsvc.AsService(d.status)
}
