// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/addons/daemon/services/dbus"
	"github.com/happy-sdk/addons/daemon/services/ipc"
	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/settings"
)

// Settings defines the top-level configuration for the system daemon, managing its working
// directory, control service, D-Bus service, inter-process communication, logging, and process
// runtime behavior.
type Settings struct {
	// WD specifies the working directory for the daemon process. It is immutable after initial
	// configuration and defaults to "{daemon.config.dir}/workspace".
	WD settings.String `key:"wd" mutation:"once"`

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
