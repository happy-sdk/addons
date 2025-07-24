// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package devel

import (
	"errors"

	"github.com/happy-sdk/addons/devel/projects"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/addon"
	"github.com/happy-sdk/happy/sdk/session"
)

var (
	Error = errors.New("devel")
)

type Settings struct {
	Projects projects.Settings `key:"projects"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	return settings.New(s)
}

func Addon(s Settings) *addon.Addon {
	api := NewAPI()
	return addon.New("Devel").
		WithConfig(addon.Config{
			Slug: "devel",
		}).
		WithSettings(&s).
		ProvideAPI(api).
		OnRegister(func(sess session.Register) error {
			return nil
		})
}
