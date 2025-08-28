// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
	"golang.org/x/sys/unix"
)

// Wrapper creates a new command with the given name and description
// what can be used for wrapping daemon commands.
func Wrapper(name, desc string) *command.Command {
	cmd := command.New(name, command.Config{
		Description:        settings.String(desc),
		SharedBeforeAction: true,
	})
	return cmd
}

// Has checks if a command with the given name exists.
func Has(name string) bool {
	return slices.Contains(ctl.AllCommands, name)
}

// Get returns command by name
func Get(name, category string) (*command.Command, error) {
	if !Has(name) {
		return nil, fmt.Errorf("command %s not found", name)
	}

	var cmd *command.Command
	switch name {
	case "health":
		// cmd = Health(category)
	case "info":
		cmd = Info(category)
	case "logs":
		cmd = Logs(category)
	case "ping":
		// cmd = Ping(category)
	case "reload":
		// cmd = Reload(category)
	case "restart":
		// cmd = Restart(category)
	case "start":
		cmd = Start(category)
	case "status":
		// cmd = Status(category)
	case "stop":
		// cmd = Stop(category)
	}

	return cmd, nil
}

// monitorProcess checks if the process with the given PID has exited and calls onExit when it does.
// It polls using unix.Kill with signal 0 and respects the context for cancellation.
func monitorProcess(ctx context.Context, pid int, onExit func()) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Check if process exists by sending signal 0
			err := unix.Kill(pid, 0)
			if err != nil {
				if errors.Is(err, unix.ESRCH) {
					onExit()
					return nil
				}
				return err
			}
		}
	}
}

func isRunning(sess *session.Context) (running bool, pid int) {
	pid = sess.Get("daemon.process.pid").Int()
	running = sess.Get("daemon.process.running").Bool()
	return
}
