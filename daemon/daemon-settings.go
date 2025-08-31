// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/addons/daemon/services/dbus"
	"github.com/happy-sdk/addons/daemon/services/ipc"
	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/fsutils"
	"github.com/happy-sdk/happy/pkg/settings"
)

// Settings defines the top-level configuration for the system daemon, managing its working
// directory, control service, D-Bus service, inter-process communication, logging, and process
// runtime behavior.
type Settings struct {
	// CTL specifies the configuration for the daemon’s control service, managing command-line
	// arguments and commands such as "start", "stop", and "status".
	CTL ctl.Settings `key:"ctl"`

	// DBus specifies the configuration for the daemon’s D-Bus service, managing bus connections
	// and service registration for system-level interactions.
	DBus dbus.Settings `key:"dbus"`

	// IPC specifies the configuration for the daemon’s socket-based inter-process communication
	// service, managing peer communication timeouts and encryption.
	IPC ipc.Settings `key:"ipc"`

	// LOG specifies the configuration for the daemon’s logging.
	LOG logd.Settings `key:"log"`

	// Process specifies the configuration for the daemon’s runtime process, managing service
	// startup, flags, and spawn strategy.
	Process process.Settings `key:"process"`

	// Paths specifies the configuration for the daemon’s file system paths and
	// enable end user to override values for specific paths.
	// These should be left default unless you know what you’re doing.
	Paths PathSettings `key:"fs.path"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	return bp, nil
}

func (s *Settings) Defaults() {
	s.CTL.Defaults()
	s.DBus.Defaults()
	s.IPC.Defaults()
	s.LOG.Defaults()
	s.Process.Defaults()
}

type PathSettings struct {
	// DirName specifies the damon default subdirectory name.
	// Used when constructing default paths.
	DirName settings.String `default:"daemon" mutation:"immutable"`

	// Cache specifies the path to the daemon’s cache directory.
	Cache settings.String `key:"cache,save" mutation:"once"`

	// Config specifies the path to the daemon’s configuration directory.
	Config settings.String `key:"config,save" mutation:"once"`

	// Data specifies the path to the daemon’s data directory.
	Data settings.String `key:"data,save" mutation:"once"`

	// Logs specifies the path to the daemon’s logs directory.
	Logs settings.String `key:"logs,save" mutation:"once"`

	// State specifies the path to the daemon’s state directory.
	State settings.String `key:"state,save" mutation:"once"`

	// WD specifies the working directory for the daemon process.
	// defaults to "{daemon.fs.path.data}/workspace".
	WD settings.String `key:"wd,save" mutation:"once"`
}

func (ps *PathSettings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(ps)
	if err != nil {
		return nil, err
	}

	bp.AddValidator("cache", "set daemon cache path", func(s settings.Setting) error {
		return ps.validatePath("cache", s.String())
	})

	bp.AddValidator("config", "set daemon config path", func(s settings.Setting) error {
		return ps.validatePath("config", s.String())
	})

	bp.AddValidator("data", "set daemon data path", func(s settings.Setting) error {
		return ps.validatePath("data", s.String())
	})

	bp.AddValidator("logs", "set daemon logs path", func(s settings.Setting) error {
		return ps.validatePath("logs", s.String())
	})

	bp.AddValidator("state", "set daemon state path", func(s settings.Setting) error {
		return ps.validatePath("state", s.String())
	})

	bp.AddValidator("wd", "set daemon working directory", func(s settings.Setting) error {
		return ps.validatePath("wd", s.String())
	})

	return bp, nil
}

func (ps *PathSettings) validatePath(vname, str string) error {
	if str == "" {
		return fmt.Errorf("%w: %s path cannot be empty", ErrPath, vname)
	}
	if !filepath.IsAbs(str) {
		return fmt.Errorf("%w: %s path must be absolute path got %s", ErrPath, vname, str)
	}
	parent := filepath.Dir(str)
	if !fsutils.IsDir(parent) {
		return fmt.Errorf("%w: %s path parent must be a existing directory writable by daemon user, got %s", ErrPath, vname, str)
	}
	if err := os.MkdirAll(str, 0750); err != nil {
		return fmt.Errorf("%w: %s path creation failed, got %s", ErrPath, vname, err)
	}
	return nil
}
