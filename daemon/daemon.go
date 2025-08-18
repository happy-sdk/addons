// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"

	"github.com/happy-sdk/addons/daemon/ipc"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/events"
	"github.com/happy-sdk/happy/sdk/session"
)

var (
	Error                  = errors.New("daemon")
	ErrAlreadyRunning      = fmt.Errorf("%w already running", Error)
	ErrNotRunning          = fmt.Errorf("%w not running", Error)
	ErrSignal              = fmt.Errorf("%w signal", Error)
	ErrInvalidOutputFormat = fmt.Errorf("%w invalid output format", Error)
)

var (
	// internal manager events
	sighupEvent = events.New("signal", "sighup")
	stopEvent   = events.New("daemon", "stop")

	StartingEvent  = events.New("daemon", "starting")
	StartedEvent   = events.New("daemon", "started")
	StoppingEvent  = events.New("daemon", "stopping")
	StoppedEvent   = events.New("daemon", "stopped")
	ReloadingEvent = events.New("daemon", "reloading")
	ReloadedEvent  = events.New("daemon", "reloaded")
)

type Settings struct {
	IPC ipc.Settings `key:"ipc"`

	// Args when set are passed to the daemon process.
	// Default application with no args is called effectively
	// calling app.Do Action
	Args settings.StringSlice

	// EnabledCommands contains the list of commands that will be provided by
	// the daemon Addon.
	EnabledCommands settings.StringSlice

	// GlobalFlags are passed to the daemon process if they are present on caller.
	// Default flags are: show-exec|system-debug|debug|verbose
	GlobalFlags settings.StringSlice `key:"global_flags" mutation:"once"`

	// LogDir specifies the directory where the daemon log file will be written.
	// Default: {daemon.config.dir}/logs
	LogDir settings.String `key:"log.dir" mutation:"once"`

	// LogFileName specifies the name of the daemon log file.
	// Default: daemon.log
	LogFileName settings.String `key:"log.file.name" default:"daemon.log" mutation:"once"`

	// SpawnStrategy specifies the strategy used to spawn the daemon process and
	// run it with appropriate process isolation based on the selected strategy.
	//
	// Default: double-fork
	// Startup Spawn Strategies:
	//
	// Foreground:
	//   - Runs daemon directly in current process without any forking
	//   - Process list shows: e.g. './app daemon start --direct'
	//   - Ideal for development, debugging, and container environments
	//
	// SingleFork:
	//   - Single-fork approach with lower resource overhead
	//   - More efficient startup with fewer system calls
	//   - Creates detached daemon that remains as child of original parent process
	//   - Suitable for application-specific background services
	//   - Command: './app daemon start --single-fork'
	//
	// DoubleFork:
	//   - Traditional Unix double-fork daemon pattern
	//   - Complete parent process independence (daemon becomes child of init)
	//   - Creates fully orphaned daemon with better process isolation
	//   - Standard approach for system daemons and long-running services
	//   - Command:  './app daemon start' or './app daemon start --double-fork'
	SpawnStrategy SpawnStrategy `key:"spawn_strategy" default:"double-fork" mutation:"once"`

	// Timeout defines the duration the daemon will wait for
	// actions to complete before failing with error.
	Timeout settings.Duration `default:"30s"`

	// WD specifies the working directory of the daemon process.
	// Default: {daemon.config.dir}/workspace
	WD settings.String `key:"wd" mutation:"once"`

	// WithWrapperCommand when set to true, the top level daemon command will
	// be added and any commands enabled will be added under the daemon as sub command.
	WithWrapperCommand settings.Bool

	Services settings.StringSlice `key:"services" default:"" mutation:"once"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}
	if err := bp.SetDefault("global_flags", &settings.StringSlice{
		"show-exec",
		"system-debug",
		"debug",
		"verbose",
	}); err != nil {
		return nil, err
	}

	return bp, nil
}

// New returns a new daemon Setup instance, to configure the daemon.
func New(s Settings) *Setup {
	return setup(&s)
}

// SpawnStrategy defines how the daemon process should be created and managed.
type SpawnStrategy int

const (
	// Foreground runs the daemon in the foreground.
	// Best for: containers, development, debugging, interactive sessions.
	Foreground SpawnStrategy = iota

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

func isRunning(sess *session.Context) (running bool, pid int) {
	pid = sess.Get("daemon.pid").Int()
	running = sess.Get("daemon.running").Bool()
	return
}
