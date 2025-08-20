// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"fmt"
	"strings"

	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/pkg/strings/textfmt"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
)

func Info(category string) *command.Command {
	cmd := command.New("info", command.Config{
		Description: "Display daemon information",
		Category:    settings.String(category),
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		t := textfmt.Table{}
		t.AddRow("DAEMON SETTINGS", "")
		t.AddDivider()
		t.AddRow("KEY", "VALUE")
		t.AddDivider()
		for setting := range sess.Settings().All() {
			if !strings.HasPrefix(setting.Key(), "daemon.") {
				continue
			}
			if setting.Kind() == settings.KindStringSlice {
				for _, value := range setting.Value().Fields() {
					t.AddRow(fmt.Sprintf("%s[]", setting.Key()), value)
				}
			} else {
				t.AddRow(setting.Key(), setting.Display())
			}
		}

		t.AddDivider()
		t.AddRow("OPTIONS", "")
		t.AddDivider()
		t.AddRow("KEY", "VALUE")
		t.AddDivider()
		for opt := range sess.Opts().All() {
			if !strings.HasPrefix(opt.Key(), "daemon.") {
				continue
			}
			t.AddRow(opt.Key(), opt.String())
		}

		fmt.Println(t.String())
		return nil
	})
	return cmd
}
