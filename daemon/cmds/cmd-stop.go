// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
)

func Stop(category string) *command.Command {
	cmd := command.New("stop", command.Config{
		Description:  "Stop (deactivate) the daemon service",
		Category:     settings.String(category),
		FailDisabled: true,
	})

	cmd.Disable(func(sess *session.Context) error {
		if running, _ := isRunning(sess); !running {
			return process.ErrNotRunning
		}
		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		return ctl.Stop(sess, args)
	})
	return cmd
}
