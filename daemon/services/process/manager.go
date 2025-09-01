// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package process

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/fsutils/pidfile"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

type Manager struct {
	pid atomic.Int64
	mu  sync.RWMutex

	startAction  action.WithArgs
	stopAction   action.WithPrevErr
	reloadAction action.WithPrevErr

	state *telemetry.DaemonState

	cancelSignalHandler context.CancelFunc

	services []string

	busy    atomic.Bool
	running atomic.Bool

	args action.Args
}

func New(state *telemetry.DaemonState) *Manager {
	pid := os.Getpid()
	state.UpdateProcessManager(func(pms *telemetry.ProcessManagerState) {
		pms.PID = pid
	})

	m := &Manager{
		state: state,
	}

	m.pid.Store(int64(pid))

	m.startAction = func(sess *session.Context, args action.Args) error {
		m.debug(sess.Log(), "skipping: no start action")
		return nil
	}
	m.stopAction = func(sess *session.Context, prevErr error) error {
		m.debug(sess.Log(), "skipping: no stop action")
		return nil
	}

	return m
}

// OnStart sets the start action for the daemon.
func (m *Manager) OnStart(action action.WithArgs) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startAction = action
}

// OnStop sets the stop action for the daemon.
func (m *Manager) OnStop(action action.WithPrevErr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopAction = action
}

// OnReload sets the reload action for the daemon. See [Setup.OnReload] for details.
func (m *Manager) OnReload(action action.WithPrevErr) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reloadAction = action
}

// WithServices registers services to be started and stopped along with the daemon.
func (m *Manager) WithServices(svcs ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.services = append(m.services, svcs...)
}

// CanStart returns true if the daemon can be started.
func (m *Manager) CanStart(sess *session.Context) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if err := m.reloadPidFile(sess); err != nil {
		m.error(sess.Log(), "checking can daemon start", slog.String("err", err.Error()))
		return false
	}
	return !sess.Get("daemon.process.running").Bool()
}

func (m *Manager) reloadPidFile(sess *session.Context) error {
	pidfilePath := sess.Opts().Get("daemon.process.pidfile").String()

	pf, err := pidfile.Open(pidfilePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: failed to open daemon pidfile: %w", Error, err)
		}
	} else {
		pid, err := pf.PID()
		defer func() {
			_ = pf.Close()
		}()
		if err != nil {
			return fmt.Errorf("%w: failed to read daemon pidfile: %s", Error, err.Error())
		}

		daemon, err := os.FindProcess(pid)
		if err != nil {
			return fmt.Errorf("%w: failed to find process by pid %d: %s", Error, pid, err.Error())
		}

		if daemon.Signal(syscall.Signal(0)) != nil {
			if err := pf.Remove(); err != nil {
				return nil
			}
		} else {
			if err := sess.Opts().Set("daemon.process.pid", pid); err != nil {
				return fmt.Errorf("failed to set daemon pid: %w", err)
			}
			if err := sess.Opts().Set("daemon.process.running", pid > 0); err != nil {
				return fmt.Errorf("failed to set daemon running status: %w", err)
			}
		}
	}

	return nil
}

func (m *Manager) pidfileCreate(sess *session.Context) (pid int, err error) {
	pidfilePath := sess.Get("daemon.process.pidfile").String()

	pf, err := pidfile.New(pidfilePath, 0, 0640)
	if err != nil {
		return 0, fmt.Errorf("%w: failed to create daemon pidfile: %w", Error, err)
	}
	defer func() {
		_ = pf.Close()
	}()

	pid, err = pf.PID()
	if err != nil {
		return pid, fmt.Errorf("%w: failed to read daemon pidfile: %w", Error, err)
	}

	if err := sess.Opts().Set("daemon.process.pid", pid); err != nil {
		return pid, fmt.Errorf("%w: failed to set daemon pid: %w", Error, err)
	}
	m.debug(sess.Log(), "created pidfile")
	return pid, nil
}

func (m *Manager) pidfileRemove(sess *session.Context) error {
	pidfilePath := sess.Get("daemon.process.pidfile").String()
	pf, err := pidfile.Open(pidfilePath)
	if err != nil {
		return fmt.Errorf("failed to open pidfile: %w", err)
	}

	fpid, _ := pf.PID()
	lpid := m.pid.Load()

	// do not allow other daemon process to remove foreign pidfile
	if int(fpid) != int(lpid) {
		return fmt.Errorf("%w: failed to remove pidfile foreign process(%d)", Error, fpid)
	}

	if err := pf.Remove(); err != nil {
		return fmt.Errorf("%w: failed to remove pidfile: %s", Error, err.Error())
	}
	m.debug(sess.Log(), "pidfile removed")
	return nil
}

func (m *Manager) osSignalHandler(sess *session.Context) {
	spawnStrategy := sess.Settings().Get("daemon.process.spawn_strategy").String()
	if spawnStrategy != Foreground.String() {
		sess.Release()
		signal.Reset()
	}

	m.debug(sess.Log(), "pass incoming system signals to a daemon process")

	ctx, cancel := context.WithCancel(sess.Context())
	m.cancelSignalHandler = cancel

	// Create a channel to receive signals
	sigChan := make(chan os.Signal, 32)
	// Notify for all signals
	signal.Notify(sigChan)

	shutdown := func(signal os.Signal, msg string) {
		sess.Destroy(fmt.Errorf("%w: %s signal(%s)", msg, signal.String()))
	}

	warning := func(signal os.Signal, msg string) {
		m.warn(sess.Log(), msg, slog.String("signal", signal.String()))
	}

	notice := func(signal os.Signal, msg string) {
		m.notice(sess.Log(), msg, slog.String("signal", signal.String()))
	}

	go func() {
		defer func() {
			signal.Stop(sigChan)
			close(sigChan)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case sig := <-sigChan:
				// Wait busy state to complete
				if m.busy.Load() {
					wait := time.NewTicker(time.Millisecond)

					for range wait.C {
						if !m.busy.Load() {
							wait.Stop()
							break
						}
					}
				}

				switch sig {
				case syscall.SIGABRT:
					shutdown(sig, "process aborted")
					return
				case syscall.SIGALRM:
					warning(sig, "timer expired")
				case syscall.SIGBUS:
					shutdown(sig, "invalid memory access")
					return
				case syscall.SIGCHLD:
					notice(sig, "child process stopped/terminated, reap child processes to avoid zombies")
				case syscall.SIGFPE:
					shutdown(sig, "floating-point exception")
					return
				case syscall.SIGHUP:
					notice(sig, "reload")
					m.reload(sess)
				case syscall.SIGILL:
					shutdown(sig, "illegal instruction")
					return
				case syscall.SIGINT, syscall.SIGTERM:
					notice(sig, "shutdown")
					sess.Destroy(nil)
					return
				case syscall.SIGIO:
					notice(sig, "I/O possible")
				case syscall.SIGPIPE:
					warning(sig, "broken pipe")
				case syscall.SIGQUIT, syscall.SIGSEGV, syscall.SIGTRAP:
					warning(sig, "shutdown")
					sess.Log().SetLevel(logging.Level(math.MinInt))
					sess.Destroy(nil)
				case syscall.SIGUSR1, syscall.SIGUSR2:
					notice(sig, "user-defined")
				}
			}
		}
	}()
}

func (m *Manager) start(sess *session.Context) error {
	if m.running.Load() {
		return ErrDaemonAlreadyRunning
	}

	m.mu.RLock()
	loadServices := m.services
	startAction := m.startAction
	args := m.args
	m.mu.RUnlock()

	if len(loadServices) > 0 {
		loader := services.NewLoader(sess, loadServices...)
		<-loader.Load()
		if err := loader.Err(); err != nil {
			m.error(sess.Log(), err.Error())
			return fmt.Errorf("%w: failed to start external services", Error)
		}
	}

	if startAction != nil {
		if err := startAction(sess, args); err != nil {
			m.error(sess.Log(), err.Error())
			return fmt.Errorf("%w: daemon start action failed", Error)
		}
	}
	m.running.Store(true)
	return nil
}

func (m *Manager) stop(sess *session.Context) error {
	if !m.running.Load() {
		return ErrDaemonNotRunning
	}

	defer m.running.Store(false)

	m.mu.RLock()
	stopServices := m.services
	stopAction := m.stopAction
	m.mu.RUnlock()

	// Collect ServiceInfo pointers upfront.
	infos := make(map[string]*service.Info, len(stopServices))
	for _, svc := range stopServices {
		info, err := sess.ServiceInfo(svc)
		if err != nil {
			m.error(sess.Log(), err.Error())
			continue
		}
		if info.Running() {
			sess.Dispatch(services.StopEvent.Create(svc, nil))
		}
		infos[svc] = info
	}

	var errs []error
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Configurable timeout.
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond) // Poll interval.
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for services to stop: %w", ctx.Err())
		case <-ticker.C:
			allStopped := true
			for svc, info := range infos {
				if info.Running() {
					allStopped = false
					continue
				}
				if e := info.Errs(); e != nil {
					for ts, err := range e {
						errs = append(errs, fmt.Errorf("service %s error at %v: %w", svc, ts, err))
						m.error(
							sess.Log(),
							fmt.Sprintf("service %s error", svc),
							slog.String("err", err.Error()),
							slog.Time("ts", ts),
						)
					}
				}
			}
			if allStopped {
				if stopAction != nil {
					if err := stopAction(sess, errors.Join(errs...)); err != nil {
						m.error(sess.Log(), err.Error())
						return fmt.Errorf("%w: daemon stop action failed", Error)
					}
				}
				return nil
			}
		}
	}
}

func (m *Manager) reload(sess *session.Context) {
	m.debug(sess.Log(), "reloading daemon...")
	if m.busy.Load() {
		wait := time.NewTicker(time.Millisecond)

		for range wait.C {
			if !m.busy.Load() {
				wait.Stop()
				break
			}
		}
	}

	m.busy.Store(true)
	m.mu.Lock()

	var err error
	if m.running.Load() {
		err = ErrDaemonNotRunning
	}
	if m.reloadAction != nil {
		if err := m.reloadAction(sess, err); err != nil {
			m.error(sess.Log(), err.Error())
			m.error(sess.Log(), "reload canceled")
			m.mu.Unlock()
			m.busy.Store(false)
			return
		}
	}

	sess.Log().NotImplemented("Manager.reload -> stop")
	sess.Log().NotImplemented("Manager.reload -> start")

	m.mu.Unlock()
	m.busy.Store(false)
}

func (m *Manager) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelDebug, ServiceName, pid, msg, args...)
}

func (m *Manager) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelInfo, ServiceName, pid, msg, args...)
}

func (m *Manager) ok(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelOk, ServiceName, pid, msg, args...)
}

func (m *Manager) warn(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelWarn, ServiceName, pid, msg, args...)
}

func (m *Manager) notice(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelNotice, ServiceName, pid, msg, args...)
}

func (m *Manager) error(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := m.pid.Load()
	logd.DaemonLog(logger, logging.LevelError, ServiceName, pid, msg, args...)
}
