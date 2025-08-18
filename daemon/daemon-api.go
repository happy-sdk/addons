// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/happy-sdk/addons/daemon/ipc"
	"github.com/happy-sdk/happy"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/pkg/strings/textfmt"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/api"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/session"
)

// API Provides API to manage the daemon from client.
type API struct {
	api.Provider
	mu         sync.RWMutex
	pid        atomic.Int64
	foreground bool
	args       action.Args
	client     *ipc.Client
}

func (s *Setup) api() *API {
	api := &API{}
	api.pid.Store(int64(os.Getpid()))
	return api
}

// Start starts the daemon.
//
// Foreground true runs the daemon directly in the current process without forking.
// Blocks until daemon is successfully started or fails with error, useful for development and container environments.
// Foreground false runs the daemon in a separate process based on SpawnStrategy setting
func (api *API) Start(sess *session.Context, args action.Args, foreground bool) error {

	api.foreground = foreground
	api.args = args

	if foreground || sess.Settings().Get("daemon.spawn_strategy").String() == Foreground.String() {
		return api.startForeground(sess, args)
	}
	return api.startBackground(sess, args)
}

func (a *API) Signal(sess *session.Context, sig syscall.Signal) error {
	running, pid := isRunning(sess)
	if !running {
		return ErrNotRunning
	}

	a.info(sess.Log(), fmt.Sprintf("sending signal %T(%s) to process %d", sig, sig.String(), pid))

	daemon, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrSignal, err.Error())
	}

	switch sig {
	case syscall.SIGTERM:
		return terminate(sess, daemon)
	default:
		return daemon.Signal(sig)
	}
}

func (a *API) Client() (*ipc.Client, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.client == nil {
		return nil, fmt.Errorf("%w: client not connected", ipc.ErrClient)
	}
	return a.client, nil
}

func (a *API) InfoTable(sess *session.Context) string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	t := textfmt.Table{}
	t.AddRow("DAEMON MANAGER SETTINGS", "")
	t.AddDivider()
	t.AddRow("KEY", "VALUE")
	t.AddDivider()
	for setting := range sess.Settings().All() {
		if !strings.HasPrefix(setting.Key(), "daemon.") {
			continue
		}
		t.AddRow(setting.Key(), setting.Display())
	}

	t.AddDivider()
	t.AddRow("OPTIONS", "")
	t.AddDivider()
	t.AddRow("KEY", "VALUE")
	t.AddDivider()
	for opt := range sess.Opts().All() {
		if !strings.HasPrefix(opt.Key(), "daemon.") {
			continue
		}
		t.AddRow(opt.Key(), opt.String())
	}
	return t.String()
}

// startForeground runs the daemon directly in the current process without forking.
func (api *API) startForeground(sess *session.Context, args action.Args) error {
	api.debug(sess.Log(), "start(foreground):")
	loader := services.NewLoader(sess, "daemon-manager")
	<-loader.Load()
	if err := loader.Err(); err != nil {
		sess.Log().Errors(err)
		return fmt.Errorf("%w: failed to start daemon", Error)
	}
	return nil
}

func (api *API) startBackground(sess *session.Context, args action.Args) error {

	var mode SpawnStrategy
	if err := settings.UnmarshalValue([]byte(sess.Settings().Get("daemon.spawn_strategy").String()), &mode); err != nil {
		return err
	}

	api.debug(sess.Log(), fmt.Sprintf("start(background): %s", mode.String()))

	switch mode {
	case DoubleFork:

		if err := api.startDoubleFork(sess); err != nil {
			return err
		}
	case SingleFork:
		if err := api.startSingleFork(sess); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown spawn strategy: %v", Error, mode)
	}

	// Loads IPC Client after daemon has launched
	loader := services.NewLoader(sess,
		"daemon-ctl",
	)
	<-loader.Load()
	if err := loader.Err(); err != nil {
		api.err(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to start daemon control service", Error)
	}

	api, err := happy.API[*API](sess)
	if err != nil {
		return err
	}
	client, err := api.Client()
	if err != nil {
		return err
	}
	if client.Connected() {
		api.debug(sess.Log(), "ipc client connected")
	}
	if _, err := client.Ping(0); err != nil {
		api.err(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to start daemon", Error)
	}
	if _, err := client.HealthCheck(); err != nil {
		api.err(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to perform daemon health check", Error)
	}
	api.ok(sess.Log(), "daemon started")
	return nil
}

func (api *API) buildForkArgs(sess *session.Context, name string) []string {
	args := []string{name}

	// global flags
	flagsToCheck := sess.Get("daemon.global_flags").Fields()
	for _, flagName := range flagsToCheck {
		if api.args.Flag(flagName).Present() {
			args = append(args, fmt.Sprintf("--%s", flagName))
		}
	}

	if sess.Get("daemon.with_wrapper_command").Bool() {
		args = append(args, "daemon")
	}
	if slices.Contains(sess.Get("daemon.enabled_commands").Fields(), "start") {
		args = append(args, "start", "--direct")
	}

	// additional args
	args = append(args, sess.Get("daemon.args").Fields()...)
	return args
}

func (api *API) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := api.pid.Load()
	log(logger, logging.LevelDebug, "daemon-ctl", pid, msg, args...)
}

func (api *API) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := api.pid.Load()
	log(logger, logging.LevelInfo, "daemon-ctl", pid, msg, args...)
}

func (api *API) ok(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := api.pid.Load()
	log(logger, logging.LevelOk, "daemon-ctl", pid, msg, args...)
}

// func (api *API) warn(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := api.pid.Load()
// 	log(logger, logging.LevelWarn, "daemon-ctl", pid, msg, args...)
// }

func (api *API) err(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := api.pid.Load()
	log(logger, logging.LevelError, "daemon-ctl", pid, msg, args...)
}

func terminate(sess *session.Context, daemonProc *os.Process) error {
	return daemonProc.Signal(syscall.SIGTERM)
}
