// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"fmt"
	"slices"
	"strings"

	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/pkg/strings/textfmt"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
)

var secrets = []string{
	"daemon.ipc.encryption_key",
}

func Info(category string) *command.Command {
	cmd := command.New("info", command.Config{
		Description: "Display daemon information",
		Category:    settings.String(category),
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {

		t := textfmt.NewTable(
			textfmt.TableTitle("DAEMON SETTINGS"),
		)
		for setting := range sess.Settings().All() {
			if !strings.HasPrefix(setting.Key(), "daemon.") {
				continue
			}
			if setting.Kind() == settings.KindStringSlice {
				for _, value := range setting.Value().Fields() {
					t.AddRow(fmt.Sprintf("%s[]", setting.Key()), value)
				}
			} else {
				if slices.Contains(secrets, setting.Key()) {
					t.AddRow(setting.Key(), "<redacted>")
				} else {
					t.AddRow(setting.Key(), setting.Display())
				}
			}
		}

		t.AddDivider()
		t.AddRow("OPTIONS", "")
		t.AddDivider()
		t.AddDivider()
		for opt := range sess.Opts().All() {
			if !strings.HasPrefix(opt.Key(), "daemon.") {
				continue
			}
			if slices.Contains(secrets, opt.Key()) {
				t.AddRow(opt.Key(), "<redacted>")
			} else {
				t.AddRow(opt.Key(), opt.String())
			}
		}

		fmt.Println(t.String())
		return nil
	})
	return cmd
}
