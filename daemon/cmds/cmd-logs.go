// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"fmt"

	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
	"github.com/happy-sdk/lib/tail"
)

func Logs(category string) *command.Command {
	cmd := command.New("logs", command.Config{
		Description: "Display daemon logs",
		Category:    settings.String(category),
	})

	cmd.WithFlags(
		cli.NewIntFlag("lines", 10, "output the last NUM lines, instead of the last 10; or use -n +NUM to skip NUM-1 lines at the start", "n"),
	)
	cmd.Do(func(sess *session.Context, args action.Args) error {
		// logfilePath := sess.Get("daemon.log.file").String()
		logfilePath := "/home/mkungla/.config/digactl/profiles/default/daemon/logs/daemon.log"

		// Allow user to cancel with Ctrl-C
		sess.Wait(false)

		lines, errs, err := tail.File(sess, logfilePath, args.Flag("lines").Var().Int())
		if err != nil {
			return err
		}

		go func() {
			for err := range errs {
				sess.Log().Error(fmt.Sprintf("TAIL: %s", err.Error()))
			}
		}()

		for line := range lines {
			fmt.Println(line)
		}

		return nil
	})
	return cmd
}
