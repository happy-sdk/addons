// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"fmt"

	"github.com/happy-sdk/addons/daemon/services/ctl"
	"github.com/happy-sdk/addons/daemon/services/process"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
)

func Start(category string) *command.Command {
	cmd := command.New("start", command.Config{
		Description:  "Start (activate) the daemon service",
		Category:     settings.String(category),
		MaxArgs:      255,
		MinArgs:      0,
		FailDisabled: true,
	})

	cmd.WithFlags(
		cli.NewBoolFlag(
			"foreground", false,
			"Run the daemon in the foreground, writing logs to stdout. Blocks until interrupted (e.g., Ctrl+C). Ideal for development and debugging.",
			"f",
		),
		cli.NewBoolFlag(
			"daemon", false,
			"Run the daemon in the background, writing logs to a configured log file. Suitable for production and systemd service integration.",
		),
		cli.NewOptionFlag(
			"mode", []string{"double-fork"}, []string{"foreground", "single-fork", "double-fork"},
			"Daemon startup mode: 'foreground' (same as --foreground), 'daemon' (same as --daemon), 'single-fork' (detached child process), or 'double-fork' (fully orphaned, adopted by init). Overrides --foreground and --daemon if specified.",
		),
	)

	cmd.Disable(func(sess *session.Context) error {
		if running, pid := isRunning(sess); running {
			return fmt.Errorf("%w: pid %d", process.ErrAlreadyRunning, pid)
		}
		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {

		var (
			hasDaemonFlag     = args.Flag("daemon").Present()
			hasForegroundFlag = args.Flag("foreground").Present()
			hasModeFlag       = args.Flag("mode").Present()
		)

		if hasDaemonFlag && hasForegroundFlag {
			return fmt.Errorf("cannot specify both --daemon and --foreground")
		}

		if (hasDaemonFlag || hasForegroundFlag) && hasModeFlag {
			return fmt.Errorf("cannot specify both --mode and --foreground/--daemon")
		}

		if args.Flag("mode").Present() {
			var mode process.SpawnStrategy
			if err := mode.UnmarshalSetting([]byte(args.Flag("mode").String())); err != nil {
				return err
			}
			if err := sess.Settings().Set("daemon.process.spawn_strategy", mode.String()); err != nil {
				return err
			}
		}

		if hasForegroundFlag {
			if err := sess.Settings().Set("daemon.process.spawn_strategy", process.Foreground.String()); err != nil {
				return err
			}
		}
		if hasDaemonFlag {
			if err := sess.Settings().Set("daemon.process.spawn_strategy", process.Daemon.String()); err != nil {
				return err
			}
		}

		strategy := sess.Settings().Get("daemon.process.spawn_strategy").String()
		if strategy == process.Daemon.String() || strategy == process.Foreground.String() {
			defer func() {
				<-sess.Wait(strategy == process.Foreground.String())
			}()
			if err := process.Start(sess, args); err != nil {
				sess.Destroy(err)
				return err
			}
			return nil
		}

		return ctl.Start(sess, args)
	})

	return cmd
}
