// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/happy-sdk/addons/daemon/cmds"
	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/addons/daemon/services/dbus"
	"github.com/happy-sdk/addons/daemon/services/ipc"
	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/logging"

	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/addon"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
)

type Setup struct {
	mu       sync.Mutex
	pid      atomic.Int64
	sealed   bool
	settings *Settings
	errs     []error

	wrapperCommand *command.Command
	customCommands map[string]*command.Command

	daemon *process.Manager
	state  *telemetry.DaemonState
}

func (s *Setup) dispose() {
	s.settings = nil
	s.errs = nil
	s.wrapperCommand = nil
	s.customCommands = nil
	s.daemon = nil
	s.state = nil
}

// New returns a new daemon Setup instance, to configure the daemon.
func New(s Settings) *Setup {
	setup := &Setup{
		settings:       &s,
		customCommands: make(map[string]*command.Command),
	}
	setup.settings.Defaults()
	setup.pid.Store(int64(os.Getpid()))
	setup.state = telemetry.NewDaemonState()
	setup.daemon = process.New(setup.state)
	return setup
}

// WithWrapperCommand sets the command to be used as a wrapper for the daemon commands.
// Daemon Command will be added as subcommands.
// When used this will set the CTL.EnableWrapperCommand true.
// If CTL.EnableWrapperCommand is true and this method is not used,
// the daemon will create default wrapper "daemon" where subcommands will be added.
func (s *Setup) WithWrapperCommand(cmd *command.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("WithWrapperCommand") {
		return
	}
	s.wrapperCommand = cmd
	s.settings.CTL.EnableWrapperCommand = true
}

// WithCommand adds a command to the daemon.
// Command will be available depending on CTL.EnableWrapperCommand. setting.
// You need to add command name to CTL.EnabledCommands settings slice
// otherwise the command will not be available.
// Custom command will override existing builtin command with the same name.
func (s *Setup) WithCommand(cmd *command.Command) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("WithCommand") {
		return
	}
	cmdName := cmd.Name()
	if _, exists := s.customCommands[cmdName]; exists {
		s.err(fmt.Sprintf("command %s already exists", cmdName))
		return
	}
	s.customCommands[cmdName] = cmd
}

// OnStart registers an action to run after all daemon services have started.
// It runs last in the startup sequence and may block, delaying full daemon readiness
func (s *Setup) OnStart(action action.WithArgs) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("OnStart") {
		return
	}
	s.daemon.OnStart(action)
}

// OnStop registers an action to run after all daemon services have stopped.
// The action receives a [*session.Context] and an error if there was any.
func (s *Setup) OnStop(action action.WithPrevErr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("OnStop") {
		return
	}
	s.daemon.OnStop(action)
}

// OnReload registers an action to run during a reload event (e.g., SIGHUP), after
// the daemon is stopped (if it was running) and before it is started again. The action
// receives a [*session.Context] and an error, which is [process.ErrDaemonNotRunning]
// if the daemon was not running. If the action returns an error, the reload is canceled,
// and the daemon will not be started. The action may block, delaying the reload.
//
// Example:
//
//	setup.OnReload(func(sess *session.Context, prevErr error) error {
//	    slog.Info("Reloading daemon configuration")
//	    if errors.Is(prevErr, process.ErrDaemonNotRunning) {
//	        slog.Warn("Daemon was not running")
//	    }
//	    return nil
//	})
func (s *Setup) OnReload(action action.WithPrevErr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("OnReload") {
		return
	}
	s.daemon.OnReload(action)
}

// WithExternalServices registers external services to be started and stopped along with the daemon.
// Services must be registered elsewhere in the application or by other addons.
func (s *Setup) WithExternalServices(svcs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.isSealed("WithExternalServices") {
		return
	}
	s.daemon.WithExternalServices(svcs...)
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

	if s.isSealed("Addon") {
		return nil
	}
	s.sealed = true

	a := addon.New("daemon").
		WithSettings(s.settings)

	opts := []*options.OptionSpec{
		options.NewOption("runtime.dir", ""),
	}
	opts = append(opts, logd.Options()...)
	opts = append(opts, ipc.Options()...)
	opts = append(opts, process.Options()...)

	a.WithOptions(opts...)

	a.ProvideCommands(s.cmds()...)

	a.OnRegister(s.addonOnRegister)

	a.ProvideServices(
		ctl.New().Service(),
		logd.New(s.state).Service(),
		s.daemon.Service(),
		ipc.New().Service(),
		dbus.New().Service(),
	)

	return a
}

func (s *Setup) addonOnRegister(sess session.Register) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.errs != nil {
		for _, err := range s.errs {
			sess.Log().Errors(err)
		}
		return fmt.Errorf("%w: daemon addon registration failed", ErrSetup)
	}

	if err := s.configurePaths(sess); err != nil {
		return fmt.Errorf("%w: %s", ErrSetup, err.Error())
	}

	// Daemon runtime directory
	daemonRuntimeDir := filepath.Join(sess.Get("app.fs.path.profile.run").String(), "daemon")
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

	s.debug(sess.Log(), "daemon addon reqistered")
	s.dispose()
	return nil
}

func (s *Setup) configurePaths(sess session.Register) error {
	var (
		profileCacheDir  = sess.Opts().Get("app.fs.path.profile.cache").String()
		profileConfigDir = sess.Opts().Get("app.fs.path.profile.config").String()
		profileDataDir   = sess.Opts().Get("app.fs.path.profile.data").String()
		profileLogsDir   = sess.Opts().Get("app.fs.path.profile.logs").String()
		profileStateDir  = sess.Opts().Get("app.fs.path.profile.state").String()
		dirName          = sess.Settings().Get("daemon.fs.path.dir_name").String()
		pathConfig       = make(map[string]string)
	)

	// Daemon cache path
	if !sess.Settings().Get("daemon.fs.path.cache").IsSet() {
		pathConfig["daemon.fs.path.cache"] = filepath.Join(profileCacheDir, dirName)
	}

	// Daemon config path
	if !sess.Settings().Get("daemon.fs.path.config").IsSet() {
		pathConfig["daemon.fs.path.config"] = filepath.Join(profileConfigDir, dirName)
	}

	// Daemon data path
	dataPath := sess.Settings().Get("daemon.fs.path.data").String()
	if !sess.Settings().Get("daemon.fs.path.data").IsSet() {
		dataPath = filepath.Join(profileDataDir, dirName)
		pathConfig["daemon.fs.path.data"] = dataPath
	}

	// Daemon logs path
	if !sess.Settings().Get("daemon.fs.path.logs").IsSet() {
		pathConfig["daemon.fs.path.logs"] = filepath.Join(profileLogsDir, dirName)
	}

	// Daemon state path
	if !sess.Settings().Get("daemon.fs.path.state").IsSet() {
		pathConfig["daemon.fs.path.state"] = filepath.Join(profileStateDir, dirName)
	}

	// Daemon working directory
	if !sess.Settings().Get("daemon.fs.path.wd").IsSet() {
		pathConfig["daemon.fs.path.wd"] = filepath.Join(dataPath, "workspace")
	}

	for k, v := range pathConfig {
		if err := sess.Settings().Set(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (s *Setup) cmds() []*command.Command {
	var (
		commands []*command.Command
		wrapper  *command.Command
		category string
	)

	if !s.settings.CTL.EnableWrapperCommand {
		category = s.settings.CTL.CommandsCategory.String()
	}

	for _, cmdName := range s.settings.CTL.EnabledCommands {
		if _, exists := s.customCommands[cmdName]; exists {
			continue
		}
		if cmds.Has(cmdName) {
			cmd, err := cmds.Get(cmdName, category)
			if err != nil {
				s.err(err.Error())
				continue
			}
			commands = append(commands, cmd)
		}
	}

	for _, cmd := range s.customCommands {
		cmd.SetCategory(category)
		commands = append(commands, cmd)
	}

	if !s.settings.CTL.EnableWrapperCommand {
		return commands
	}

	if s.wrapperCommand != nil {
		wrapper = s.wrapperCommand
	} else {
		wrapper = cmds.Wrapper(
			s.settings.CTL.WrapperCommandName.String(),
			s.settings.CTL.WrapperCommandDescription.String(),
		)
		if len(commands) == 0 {
			wrapper.Disable(func(sess *session.Context) error {
				return fmt.Errorf("%w: no daemon commands attached", Error)
			})
			wrapper.Do(func(sess *session.Context, args action.Args) error {
				return nil
			})
		}
	}

	wrapper.WithSubCommands(commands...)

	return []*command.Command{wrapper}
}

func (s *Setup) isSealed(method string) bool {
	if s.sealed {
		s.errSealed(method)
		return true
	}
	return false
}

func (s *Setup) errSealed(method string) {
	pid := s.pid.Load()
	s.errs = append(s.errs, fmt.Errorf("%w: Setup(%d).%s: called after Setup was sealed or already used", ErrSetup, pid, method))
}

func (s *Setup) err(msg string) {
	pid := s.pid.Load()
	s.errs = append(s.errs, fmt.Errorf("%w: Setup(%d): %s", ErrSetup, pid, msg))
}

func (s *Setup) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	logd.DaemonLog(logger, logging.LevelDebug, "daemon-setup", pid, msg, args...)
}

func (s *Setup) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	logd.DaemonLog(logger, logging.LevelInfo, "daemon-setup", pid, msg, args...)
}

func (s *Setup) error(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	logd.DaemonLog(logger, logging.LevelError, "daemon-setup", pid, msg, args...)
}
