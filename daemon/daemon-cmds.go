// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/happy-sdk/happy"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/session"
)

var AllCommands = settings.StringSlice{
	"ping",
	"reload",
	"restart",
	"start",
	"status",
	"stop",
}

func (s *Setup) cmds() []*command.Command {

	var (
		cmds []*command.Command
		cat  string
	)

	if !s.settings.WithWrapperCommand {
		cat = "DAEMON"
	}
	var cfuncs = map[string]func(cat string) *command.Command{
		"ping":    CommandPing,
		"reload":  CommandReload,
		"restart": CommandRestart,
		"start":   CommandStart,
		"status":  CommandStatus,
		"stop":    CommandStop,
	}

	for _, cmdName := range s.settings.EnabledCommands {
		cfunc, ok := cfuncs[cmdName]
		if ok {
			cmds = append(cmds, cfunc(cat))
		}
	}

	if !s.settings.WithWrapperCommand {
		return cmds
	}

	return []*command.Command{Command(cmds)}
}

func Command(cmds []*command.Command) *command.Command {
	cmd := command.New("daemon", command.Config{
		Description:        "Control the application daemon service",
		SharedBeforeAction: true,
	})
	cmd.WithSubCommands(cmds...)
	return cmd
}

func CommandPing(category string) *command.Command {
	cmd := command.New("ping", command.Config{
		Description: "Ping the daemon service",
		Category:    settings.String(category),
	})

	cmd.Disable(func(sess *session.Context) error {
		if running, _ := isRunning(sess); !running {
			return ErrNotRunning
		}
		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		loader := services.NewLoader(sess,
			"daemon-ctl",
		)
		<-loader.Load()
		if err := loader.Err(); err != nil {
			return fmt.Errorf("%w: failed to start daemon control service", Error)
		}

		api, err := happy.API[*API](sess)
		if err != nil {
			return err
		}

		client, err := api.Client()
		if err != nil {
			return err
		}

		prevPongSeq := uint64(0)
		for range 4 {
			if pong, err := client.Ping(prevPongSeq); err != nil {
				api.err(sess.Log(), err.Error())
				return fmt.Errorf("%w: failed to start daemon", Error)
			} else {
				prevPongSeq = pong.Seq()
				sess.Log().Printf("%d bytes from daemon (%s): seq=%d time=%.3f ms",
					pong.Len(),
					pong.Addr(),
					prevPongSeq,
					pong.Duration().Seconds()*1000,
				)
			}
			time.Sleep(time.Second)
		}

		return nil
	})
	return cmd
}

func CommandReload(category string) *command.Command {
	cmd := command.New("reload", command.Config{
		Description: "Reload the daemon service",
		Category:    settings.String(category),
	})

	cmd.Disable(func(sess *session.Context) error {
		if running, _ := isRunning(sess); !running {
			return ErrNotRunning
		}
		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		api, err := happy.API[*API](sess)
		if err != nil {
			return err
		}

		return api.Signal(sess, syscall.SIGHUP)
	})
	return cmd
}

func CommandRestart(category string) *command.Command {
	cmd := command.New("restart", command.Config{
		Description: "Restart the daemon service",
		Category:    settings.String(category),
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		sess.Log().NotImplemented("restart not impl")
		return nil
	})
	return cmd
}

func CommandStart(category string) *command.Command {
	cmd := command.New("start", command.Config{
		Description:      "Start (activate) the daemon service",
		Category:         settings.String(category),
		MaxArgs:          255,
		MinArgs:          0,
		FailDisabled:     true,
		SkipSharedBefore: true,
	})

	cmd.WithFlags(
		cli.NewBoolFlag(
			"direct", false,
			"Run daemon in foreground (alias for --mode=foreground). Blocks until daemon exits, useful for development and debugging",
			"d",
		),
		cli.NewOptionFlag(
			"mode", []string{"double-fork"}, []string{"foreground", "single-fork", "double-fork"},
			"Daemon startup mode: 'foreground' runs in current process (development/containers), 'single-fork' creates detached daemon as child process (efficient), 'double-fork' creates fully orphaned daemon adopted by init (production standard)",
		),
	)

	cmd.Disable(func(sess *session.Context) error {
		if running, pid := isRunning(sess); running {
			return fmt.Errorf("%w: pid %d", ErrAlreadyRunning, pid)
		}

		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {

		if args.Flag("mode").Present() {
			var mode SpawnStrategy
			if err := mode.UnmarshalSetting([]byte(args.Flag("mode").String())); err != nil {
				return err
			}
			if err := sess.Settings().Set("daemon.mode", mode.String()); err != nil {
				return err
			}
		}

		direct := args.Flag("direct").Var().Bool()

		defer func() {
			if direct {
				<-sess.Wait(false)
			}
		}()

		api, err := happy.API[*API](sess)
		if err != nil {
			return err
		}

		return api.Start(sess, args, direct)
	})

	return cmd
}

func CommandStatus(category string) *command.Command {
	cmd := command.New("status", command.Config{
		Description:  "Check the status of the daemon service",
		Category:     settings.String(category),
		FailDisabled: true,
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		api, err := happy.API[*API](sess)
		if err != nil {
			return err
		}

		if err := api.Signal(sess, 0); err != nil {
			if !errors.Is(err, ErrNotRunning) {
				return err
			}
			fmt.Println("inactive")
			return nil
		}
		fmt.Println("active")
		return nil
	})
	return cmd
}

func CommandStop(category string) *command.Command {
	cmd := command.New("stop", command.Config{
		Description:  "Stop (deactivate) the daemon service",
		Category:     settings.String(category),
		FailDisabled: true,
	})

	cmd.Disable(func(sess *session.Context) error {
		if running, _ := isRunning(sess); !running {
			return ErrNotRunning
		}
		return nil
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		api, err := happy.API[*API](sess)
		if err != nil {
			return err
		}

		return api.Signal(sess, syscall.SIGTERM)
	})
	return cmd
}
