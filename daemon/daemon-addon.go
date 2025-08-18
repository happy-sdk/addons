// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/happy-sdk/happy/pkg/fsutils/pidfile"
	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/sdk/addon"
	"github.com/happy-sdk/happy/sdk/session"
)

func (s *Setup) addon() *addon.Addon {
	a := addon.New("daemon").
		WithSettings(s.settings)

	api := s.api()
	a.ProvideAPI(api)

	a.ProvideCommands(s.cmds()...)

	inst := newInstance(s, api)

	a.ProvideServices(
		// Client service(s)
		api.controlService(),
		// Daemon Service(s)
		inst.managerService(),
		inst.ipcService(),
		inst.workerService(),
	)

	a.WithOptions(
		options.NewOption("config.dir", ""),
		options.NewOption("ctl.socket", ""),
		options.NewOption("log.file.path", ""),
		options.NewOption("pid", 0),
		options.NewOption("pidfile.path", ""),
		options.NewOption("running", false),
		options.NewOption("runtime.dir", ""),
	)

	a.WithEvents(
		stopEvent,
		sighupEvent,
		StartingEvent,
		StartedEvent,
		StoppingEvent,
		StoppedEvent,
		ReloadingEvent,
		ReloadedEvent,
	)

	a.OnRegister(s.addonOnRegister)
	return a
}

func (s *Setup) addonOnRegister(sess session.Register) error {
	// Daemon config directory
	daemonConfigDir := filepath.Join(sess.Get("app.fs.path.profile").String(), "daemon")
	if stat, err := os.Stat(daemonConfigDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(daemonConfigDir, 0750); err != nil {
				return fmt.Errorf("failed to create daemon config directory: %w", err)
			}
		} else {
			return err
		}
	} else if !stat.IsDir() {
		return fmt.Errorf("%w: not a directory: %s", Error, daemonConfigDir)
	}
	if err := sess.Opts().Set("daemon.config.dir", daemonConfigDir); err != nil {
		return fmt.Errorf("failed to set daemon pidfile path: %w", err)
	}

	// Daemon runtime directory
	daemonRuntimeDir := filepath.Join(sess.Get("app.fs.path.run").String(), "daemon")
	if stat, err := os.Stat(daemonRuntimeDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(daemonRuntimeDir, 0750); err != nil {
				return fmt.Errorf("failed to create daemon runtime directory: %w", err)
			}
		} else {
			return err
		}
	} else if !stat.IsDir() {
		return fmt.Errorf("%w: not a directory: %s", Error, daemonRuntimeDir)
	}

	if err := sess.Opts().Set("daemon.runtime.dir", daemonRuntimeDir); err != nil {
		return fmt.Errorf("failed to set daemon pidfile path: %w", err)
	}

	// Daemon working directory
	var (
		wd    string
		wdSet bool
	)

	if !sess.Get("daemon.wd").Empty() {
		wd = sess.Get("daemon.wd").String()
	} else {
		wd = filepath.Join(daemonConfigDir, "workspace")
	}

	if err := os.MkdirAll(wd, 0750); err != nil {
		return fmt.Errorf("failed to create daemon working directory: %w", err)
	}
	if !wdSet {
		if err := sess.Settings().Set("daemon.wd", wd); err != nil {
			return fmt.Errorf("failed to set daemon working directory: %w", err)
		}
	}

	// Set pid file path
	pidfilePath := filepath.Join(daemonRuntimeDir, "daemon.pid")
	if err := sess.Opts().Set("daemon.pidfile.path", pidfilePath); err != nil {
		return fmt.Errorf("failed to set daemon pidfile path: %w", err)
	}

	pf, err := pidfile.Open(pidfilePath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed to open daemon pidfile: %w", err)
		}
	} else {
		pid, err := pf.PID()
		defer func() {
			_ = pf.Close()
		}()
		if err != nil {
			return fmt.Errorf("failed to read daemon pidfile: %w", err)
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
			if err := sess.Opts().Set("daemon.pid", pid); err != nil {
				return fmt.Errorf("failed to set daemon pid: %w", err)
			}
			if err := sess.Opts().Set("daemon.running", pid > 0); err != nil {
				return fmt.Errorf("failed to set daemon running status: %w", err)
			}
		}
	}

	// Daemon log directory
	var logdir string
	if sess.Settings().Get("daemon.log.dir").IsSet() {
		logdir = sess.Settings().Get("daemon.log.dir").String()
	} else {
		logdir = filepath.Join(sess.Get("daemon.config.dir").String(), "logs")
		if err := sess.Settings().Set("daemon.log.dir", logdir); err != nil {
			return fmt.Errorf("failed to set daemon log directory: %w", err)
		}
	}
	if err := os.MkdirAll(logdir, 0750); err != nil {
		sess.Log().Errors(err)
		return fmt.Errorf("%w: failed to create log directory", Error)
	}

	// Daemon log file
	if err := sess.Opts().Set("daemon.log.file.path", filepath.Join(
		logdir,
		sess.Settings().Get("daemon.log.file.name").String(),
	)); err != nil {
		return fmt.Errorf("failed to set daemon log file path: %w", err)
	}

	// ctlSocketPath := filepath.Join(daemonRuntimeDir, "daemon-ctl.sock")
	ctlSocketPath := fmt.Sprintf("@%s-daemon.sock", sess.Get("app.slug").String())
	if err := sess.Opts().Set("daemon.ctl.socket", ctlSocketPath); err != nil {
		return fmt.Errorf("failed to set daemon ctl socket path: %w", err)
	}
	s.info(sess.Log(), "configuration valid")
	return nil
}
