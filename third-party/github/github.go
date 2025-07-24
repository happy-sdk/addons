// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2022 The Happy Authors

package github

import (
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/addon"
)

type Settings struct {
	Owner          settings.String `key:"owner" default:"octocat" mutation:"once"`
	Repo           settings.String `key:"repo" default:"hello-worId" mutation:"once"`
	CommandEnabled settings.Bool   `key:"command.enabled" default:"false" mutation:"once"`
}

func (s Settings) Blueprint() (*settings.Blueprint, error) {
	return settings.New(s)
}

type Github struct{}

func Addon(s Settings) *addon.Addon {
	addon := addon.New("github").WithSettings(s)

	return addon
}
