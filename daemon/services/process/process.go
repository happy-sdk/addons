// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package process provides configuration and api's for the daemon's runtime process.
package process

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/session"
)

const ServiceSlug = "daemon-process"

var GlobalFlags = settings.StringSlice{
	"show-exec",
	"system-debug",
	"debug",
	"verbose",
}

var (
	Error                   = fmt.Errorf(ServiceSlug)
	ErrAlreadyRunning       = fmt.Errorf("%w already running", Error)
	ErrNotRunning           = fmt.Errorf("%w not running", Error)
	ErrDaemonNotRunning     = errors.New("daemon not running")
	ErrDaemonAlreadyRunning = errors.New("daemon already running")
)

// Settings defines the configuration for the daemon's runtime process, controlling
// external service management, inherited flags, spawn strategy, and timeouts. It is
// used to customize how the daemon starts and runs.
type Settings struct {
	// ExternalServices lists external services to be started or stopped by the daemon,
	// each identified by a service name or address.
	// These services must be registered by addons or other components.
	ExternalServices settings.StringSlice

	// InheritedFlags specifies command-line flags passed to the daemon process if
	// present in the caller. Supported flags include onse listed in GlobalFlags
	InheritedFlags settings.StringSlice

	// SpawnStrategy determines the process isolation strategy for starting the daemon.
	// Supported strategies are:
	//   - Foreground: Runs in the current process without forking, ideal for
	//     development or containers (e.g., "./app daemon start --direct").
	//   - SingleFork: Uses a single fork for lower overhead, creating a detached
	//     daemon as a child of the parent (e.g., "./app daemon start --single-fork").
	//   - DoubleFork: Uses the traditional Unix double-fork pattern for full
	//     independence, becoming a child of init/systemd (e.g., "./app daemon start").
	// Defaults to DoubleFork.
	SpawnStrategy SpawnStrategy `default:"double-fork" mutation:"once"`

	// Timeout specifies the duration the daemon waits for actions to complete before
	// failing with an error. Defaults to 30 seconds.
	Timeout settings.Duration `default:"30s"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	if err := bp.SetDefault("inherited_flags", &GlobalFlags); err != nil {
		return nil, err
	}

	return bp, nil
}

func (s *Settings) Defaults() {}

func Options() []*options.OptionSpec {
	return []*options.OptionSpec{
		options.NewOption("process.pid", 0),
		options.NewOption("process.pidfile", ""),
		options.NewOption("process.running", false),
	}
}

// SpawnStrategy defines how the daemon process should be created and managed.
type SpawnStrategy int

const (
	// Foreground runs the daemon in the foreground.
	// Best for: containers, development, debugging, interactive sessions.
	Foreground SpawnStrategy = iota

	// Daemon runs the daemon in the current process (no forking), writing logs to a file.
	// Sets up session leader (setsid) and redirects stdio. Triggered by --daemon or --mode=daemon.
	// Best for: production with systemd, final daemon process after forking.
	Daemon

	// SingleFork uses single-fork for efficient daemon creation.
	// Creates detached daemon that remains as child of the original parent process.
	// Best for: performance-critical applications, application services, faster startup.
	SingleFork

	// DoubleFork uses double-fork for complete parent independence.
	// Creates fully orphaned daemon that becomes direct child of init (traditional Unix pattern).
	// Best for: production system services, long-running processes, init integration.
	DoubleFork
)

// String returns a human-readable representation of the Mode.
func (m SpawnStrategy) String() string {
	switch m {
	case Foreground:
		return "foreground"
	case Daemon:
		return "daemon"
	case SingleFork:
		return "single-fork"
	case DoubleFork:
		return "double-fork"
	default:
		return "unknown"
	}
}

func (m *SpawnStrategy) UnmarshalSetting(data []byte) error {
	str := string(data)
	switch str {
	case "foreground":
		*m = Foreground
	case "single-fork":
		*m = SingleFork
	case "double-fork":
		*m = DoubleFork
	default:
		return fmt.Errorf("invalid spawn strategy: %s", str)
	}
	return nil
}

func (m SpawnStrategy) MarshalSetting() ([]byte, error) {
	return []byte(m.String()), nil
}

func (m *SpawnStrategy) Is(other SpawnStrategy) bool {
	return *m == other
}

var (
	noFork = []string{Foreground.String(), Daemon.String()}
)

// Start starts the daemon.
//
// Foreground true runs the daemon directly in the current process without forking.
// Blocks until daemon is successfully started or fails with error.
func Start(sess *session.Context, args action.Args) error {
	spawnStrategy := sess.Settings().Get("daemon.process.spawn_strategy").String()
	if slices.Contains(noFork, spawnStrategy) {
		return startForeground(sess, args)
	}

	return fmt.Errorf("%w: can not start %s with %s strategy", Error, ServiceSlug, spawnStrategy)
}

var daemonArgs sync.Map

// startForeground runs the daemon directly in the current process without forking.
func startForeground(sess *session.Context, args action.Args) error {
	logDebug(sess.Log(), "start(foreground):")

	key := sess.Opts().Get("app.instance.id").String()

	addArgs := sess.Settings().Get("daemon.ctl.args").Value().Fields()
	if len(addArgs) > 0 {
		var err error
		args, err = action.EnsureArgs(args, addArgs...)
		if err != nil {
			return fmt.Errorf("%w: failed to compose daemon args", err.Error())
		}
	}
	daemonArgs.Store(key, args)

	loader := services.NewLoader(sess, "daemon-process")
	<-loader.Load()
	if err := loader.Err(); err != nil {
		err := fmt.Errorf("%w: failed to start daemon %s", Error, err.Error())
		daemonArgs.Clear()
		return err
	}

	return nil
}

func logDebug(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := os.Getpid()
	logd.DaemonLog(logger, logging.LevelDebug, ServiceSlug, int64(pid), msg, args...)
}

func logError(logger *logging.Logger, msg string, args ...slog.Attr) {
	pid := os.Getpid()
	logd.DaemonLog(logger, logging.LevelError, ServiceSlug, int64(pid), msg, args...)
}
