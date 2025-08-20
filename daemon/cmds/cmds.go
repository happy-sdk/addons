// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"fmt"
	"slices"

	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/cli/command"
)

var All = settings.StringSlice{
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
	return slices.Contains(All, name)
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
		// cmd = Start(category)
	case "status":
		// cmd = Status(category)
	case "stop":
		// cmd = Stop(category)
	}

	return cmd, nil
}
