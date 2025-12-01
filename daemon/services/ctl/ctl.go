// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package ctl provides daemon's control service.
package ctl

import (
	"fmt"
	"log/slog"
	"os"
	"slices"
	"syscall"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/session"
)

const ServiceName = "daemon-ctl"

var (
	Error = fmt.Errorf(ServiceName)
)

var AllCommands = settings.StringSlice{
	"health",
	"info",
	"logs",
	"ping",
	"reload",
	"restart",
	"start",
	"status",
	"stop",
}

// Settings defines the configuration for the daemon's control service, managing
// command-line arguments, enabled commands, and wrapper command behavior. It is
// used to customize how the daemon exposes control commands via CLI or D-Bus
// interfaces.
type Settings struct {
	// Args specifies command-line arguments passed to the daemon process.
	Args settings.StringSlice

	// EnabledCommands lists commands provided by the daemon addon, such as
	// "health", "info", "ping", "reload", "restart", "start", "status", or
	// "stop".
	EnabledCommands settings.StringSlice

	// CommandsCategory groups enabled commands under a category name when
	// EnableWrapperCommand is true. Defaults to "DAEMON".
	CommandsCategory settings.String

	// EnableWrapperCommand, when true, adds a top-level daemon command with
	// enabled commands as subcommands. Defaults to false.
	EnableWrapperCommand settings.Bool

	// WrapperCommandName sets the name of the top-level daemon command when
	// EnableWrapperCommand is true. Defaults to "daemon". Ignored if a custom
	// wrapper command is defined.
	WrapperCommandName settings.String

	// WrapperCommandDescription describes the top-level daemon command when
	// EnableWrapperCommand is true. Defaults to "Control the application daemon
	// service"
	WrapperCommandDescription settings.String
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	if err := bp.SetDefault("enabled_commands", &AllCommands); err != nil {
		return nil, err
	}

	if err := bp.SetDefault("commands_category", &defaultCategory); err != nil {
		return nil, err
	}

	return bp, nil
}

var defaultCategory = settings.String("DAEMON")

func (s *Settings) Defaults() {
	if s.EnableWrapperCommand && s.CommandsCategory == "" {
		s.CommandsCategory = defaultCategory
	}

	if s.WrapperCommandName == "" {
		s.WrapperCommandName = "daemon"
	}

	if s.WrapperCommandDescription == "" {
		s.WrapperCommandDescription = "Control the application daemon service"
	}
}

func Start(sess *session.Context, args action.Args) error {
	if sess.Opts().Get("daemon.process.running").Variable().Bool() {
		return process.ErrDaemonAlreadyRunning
	}

	var mode process.SpawnStrategy
	if err := settings.UnmarshalValue([]byte(sess.Settings().Get("daemon.process.spawn_strategy").String()), &mode); err != nil {
		return err
	}
	logDebug(sess.Log(), fmt.Sprintf("start(background): %s", mode.String()))

	switch mode {
	case process.SingleFork:
		if err := startSingleFork(sess, args); err != nil {
			return err
		}
	case process.DoubleFork:
		if err := startDoubleFork(sess, args); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown spawn strategy: %v", Error, mode)
	}
	sess.Log().Log(sess.Context(), logging.LevelNotImpl.Level(), "wait startup")
	return nil
}

func Stop(sess *session.Context, args action.Args) error {
	if !sess.Opts().Get("daemon.process.running").Variable().Bool() {
		return process.ErrDaemonNotRunning
	}

	pid := sess.Opts().Get("daemon.process.pid").Variable().Int()

	daemon, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("%w: failed to find process by pid %d: %s", Error, pid, err.Error())
	}
	if err := daemon.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	sess.Log().Log(sess.Context(), logging.LevelNotImpl.Level(), "wait shutdown")
	return nil
}

func buildForkArgs(sess *session.Context, name string, args action.Args) []string {
	nargs := []string{name}

	// global flags
	flagsToCheck := sess.Get("daemon.process.inherited_flags").Fields()
	for _, flagName := range flagsToCheck {
		if args.Flag(flagName).Present() {
			nargs = append(nargs, fmt.Sprintf("--%s", flagName))
		}
	}

	if sess.Get("daemon.ctl.enable_wrapper_command").Bool() {
		nargs = append(nargs, sess.Get("daemon.ctl.wrapper_command_name").String())
	}

	if slices.Contains(sess.Get("daemon.ctl.enabled_commands").Fields(), "start") {
		nargs = append(nargs, "start", "--daemon")
	}
	nargs = append(nargs, sess.Get("daemon.ctl.args").Fields()...)
	return nargs
}

func logDebug(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := os.Getpid()
	logd.DaemonLog(logger, logging.LevelDebug, ServiceName, int64(pid), msg, args...)
}

func logInfo(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := os.Getpid()
	logd.DaemonLog(logger, logging.LevelInfo, ServiceName, int64(pid), msg, args...)
}

func logError(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := os.Getpid()
	logd.DaemonLog(logger, logging.LevelError, ServiceName, int64(pid), msg, args...)
}
