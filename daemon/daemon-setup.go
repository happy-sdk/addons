// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"github.com/happy-sdk/addons/daemon/healthcheck"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/addon"
	"github.com/happy-sdk/happy/sdk/services"
)

type Setup struct {
	mu       sync.Mutex
	pid      atomic.Int64
	sealed   bool
	settings *Settings

	cronSetup   func(schedule services.CronScheduler)
	status      *healthcheck.Status
	startAction action.WithArgs
	stopAction  action.WithPrevErr
}

func setup(s *Settings) *Setup {
	setup := &Setup{
		settings: s,
		status:   healthcheck.NewStatus(),
	}
	setup.pid.Store(int64(os.Getpid()))
	return setup
}

// OnStart sets the start action for the daemon.
func (s *Setup) OnStart(action action.WithArgs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		s.initerr("OnStart", "setting start action")
		return
	}
	s.startAction = action
}

// OnStop sets the stop action for the daemon.
func (s *Setup) OnStop(action action.WithPrevErr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		s.initerr("OnStop", "setting stop action")
		return
	}
	s.stopAction = action
}

func (s *Setup) HealthCheck(f healthcheck.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = healthcheck.WithHandlerFunc(s.status, f)
}

func (s *Setup) Cron(setupFunc func(schedule services.CronScheduler)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		s.initerr("OnStop", "setting stop action")
		return
	}
	s.cronSetup = setupFunc
}

// Addon builds and returns a Happy SDK addon configured with the current Setup.
//
// This method performs the final composition of all configured components into
// a ready-to-use addon instance. It enforces single-use semantics to prevent
// configuration inconsistencies.
//
// Important usage constraints:
//   - Must be called exactly once per Setup instance
//   - The Setup becomes invalid and should not be used after calling Addon
//   - Subsequent calls return nil and have no effect
func (s *Setup) Addon() *addon.Addon {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		s.initerr("Addon", "creating daemon addon")
		return nil
	}
	return s.addon()
}

// func (s *Setup) debug(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := s.pid.Load()
// 	log(logger, logging.LevelDebug, "daemon-setup", pid, msg, args...)
// }

func (s *Setup) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	log(logger, logging.LevelInfo, "daemon-setup", pid, msg, args...)
}

// func (s *Setup) ok(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := s.pid.Load()
// 	log(logger, logging.LevelOk, "daemon-setup", pid, msg, args...)
// }

// func (s *Setup) err(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := s.pid.Load()
// 	log(logger, logging.LevelError, "daemon-setup", pid, msg, args...)
// }

func (s *Setup) initerr(method, msg string) {
	pid := s.pid.Load()
	slog.Error(fmt.Sprintf("daemon-setup(%d).%s: %s (called after *Setup sealed)", pid, method, msg))
}
