// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package process

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/happy"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

func (m *Manager) Service() *services.Service {
	m.mu.Lock()
	defer m.mu.Unlock()

	svc := services.New(service.Config{
		Name:          "daemon-process",
		Description:   "Daemon process control service",
		RetryOnError:  false,
		MaxRetries:    3,
		RetryBackoff:  settings.Duration(5 * time.Second),
		LoaderTimeout: settings.Duration(time.Second * 5),
	})

	svc.OnRegister(m.onRegister)
	svc.OnStart(m.onStart)
	svc.OnStop(m.onStop)

	return svc
}

// onRegister daemon process manager service
func (m *Manager) onRegister(sess *session.Context) error {
	// Set pid file path

	pidfileName := fmt.Sprintf(
		"%s-daemon.pid",
		sess.Opts().Get("app.profile.name").String(),
	)
	pidfilePath := filepath.Join(
		sess.Opts().Get("app.fs.path.pids").String(),
		pidfileName,
	)

	if err := sess.Opts().Set("daemon.process.pidfile", pidfilePath); err != nil {
		return fmt.Errorf("failed to set daemon pidfile path: %w", err)
	}

	info, err := sess.ServiceInfo(ServiceName)
	if err != nil {
		return err
	}

	if err := m.reloadPidFile(sess); err != nil {
		return err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	m.state.UpdateProcessManager(func(ls *telemetry.ProcessManagerState) {
		ls.Service.Name = info.Name()
		ls.Service.Addr = info.Addr().String()
		ls.Service.Errors = info.Errs()
		ls.Service.Status = telemetry.ServiceStatusStopped
		if sess.Opts().Get("daemon.process.running").Variable().Bool() {
			ls.Service.Status = telemetry.ServiceStatusRunning
		}
	})
	m.debug(sess.Log(), "daemon-process service registered")
	return nil
}

func (m *Manager) onStart(sess *session.Context) (err error) {
	if !m.CanStart(sess) {
		return ErrAlreadyRunning
	}

	startedAt := time.Now()
	m.debug(sess.Log(), fmt.Sprintf("starting %s daemon process...", sess.Get("app.slug")))

	defer func() {
		m.handleStateDeferError(sess, err)
	}()

	m.mu.Lock()

	state := m.state
	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		pms.Busy = true
		pms.Service.Status = telemetry.ServiceStatusStarting
		m.busy.Store(true)
	})

	_, err = m.pidfileCreate(sess)

	key := sess.Opts().Get("app.instance.id").String()
	if v, ok := daemonArgs.Load(key); ok {
		args, ok := v.(action.Args)
		if !ok {
			return fmt.Errorf("%w: invalid daemon arguments", Error)
		}
		m.args = args
		daemonArgs.Clear()
	}

	m.mu.Unlock()

	if err != nil {
		return err
	}

	if sess.Settings().Get("daemon.log.disabled").Value().Bool() {
		state.UpdateLogger(func(ls *telemetry.LoggerState) {
			ls.Service.Status = telemetry.ServiceStatusDisabled
		})
	} else {
		// Load logging service
		m.notice(sess.Log(), "switching to file-only logging", slog.String("file", sess.Opts().Get("daemon.log.file").String()))
		loader := happy.ServiceLoader(sess, "daemon-logd")
		<-loader.Load()
		if err := loader.Err(); err != nil {
			return fmt.Errorf("failed to load logging service: %w", err)
		}
	}

	defer func() {
		state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
			pms.Busy = false
			m.busy.Store(false)
		})
	}()
	m.mu.Lock()
	// Initialize signal handler
	m.osSignalHandler(sess)
	m.mu.Unlock()
	ipcLoader := services.NewLoader(sess, "daemon-ipc")
	<-ipcLoader.Load()
	if err := ipcLoader.Err(); err != nil {
		m.error(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to start IPC service", Error)
	}

	if sess.Settings().Get("daemon.dbus.enabled").Value().Bool() {
		dbusLoader := services.NewLoader(sess, "daemon-dbus")
		<-dbusLoader.Load()
		if err := dbusLoader.Err(); err != nil {
			m.error(sess.Log(), err.Error())
			return fmt.Errorf("%w: failed to start D-Bus service", Error)
		}
	} else {
		m.debug(sess.Log(), "D-Bus service is disabled")
	}

	if err := m.start(sess); err != nil {
		return err
	}

	// READY
	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		pms.Service.Status = telemetry.ServiceStatusRunning
		pms.Service.StartUpTook = time.Since(startedAt)
	})

	took := state.ProcessManager().Service.StartUpTook
	m.ok(
		sess.Log(),
		fmt.Sprintf("%s daemon process startup took %s", sess.Get("app.slug"), took),
		slog.Duration("took", took),
	)
	return nil
}

func (m *Manager) onStop(sess *session.Context, err error) (serr error) {
	m.debug(sess.Log(), fmt.Sprintf("%s daemon process stopping...", sess.Get("app.slug")))

	m.mu.RLock()

	state := m.state

	// Stop listening signals
	if m.cancelSignalHandler != nil {
		m.cancelSignalHandler()
	}

	m.mu.RUnlock()

	defer func() {
		m.handleStateDeferError(sess, err)
	}()

	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		pms.Busy = true
		pms.Service.Status = telemetry.ServiceStatusStopping
	})

	if err := m.stop(sess); err != nil && !errors.Is(err, ErrDaemonNotRunning) {
		return err
	}

	defer func() {
		state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
			pms.Busy = false
		})
	}()

	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		pms.Service.Status = telemetry.ServiceStatusStopped
		pms.Service.StoppedAt = time.Now()
	})

	m.mu.RLock()
	err = m.pidfileRemove(sess)
	m.mu.RUnlock()
	if err == nil {
		m.ok(sess.Log(), fmt.Sprintf("%s daemon process stopped", sess.Get("app.slug")))
	}
	return err
}

func (m *Manager) handleStateDeferError(sess *session.Context, err error) {
	m.mu.RLock()
	state := m.state
	m.mu.RUnlock()
	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		info, infoErr := sess.ServiceInfo(ServiceName)
		if infoErr != nil {
			pms.AddError(infoErr)
			return
		}
		pms.Service.Errors = info.Errs()
		if err != nil {
			pms.AddError(err)
		}
		if len(pms.Service.Errors) > 0 {
			pms.Service.Status = telemetry.ServiceStatusFailed
		}
	})
}
