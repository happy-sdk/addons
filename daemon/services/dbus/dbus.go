// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package dbus provides the daemon's D-Bus service.
package dbus

import (
	"fmt"
	"strings"

	"github.com/happy-sdk/happy/pkg/settings"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

const ServiceName = "daemon-dbus"

var (
	Error = fmt.Errorf(ServiceName)
)

// Settings defines the configuration for the daemon's D-Bus service, managing bus connections
// and service registration for system-level interactions in a Unix environment, often used
// for integration with GNOME extensions.
type Settings struct {
	// Enabled marks whether the D-Bus service is enabled.
	Enabled settings.Bool `default:"false"`
	// BusType specifies the D-Bus bus to connect to: "system" for system-wide services
	// or "session" for user-scoped services. Defaults to "session".
	BusType settings.String `default:"session" mutation:"once"`

	// ServiceName specifies the D-Bus service name for the daemon.
	ServiceName settings.String `default:"com.example.MyService1" mutation:"once"`

	// ObjectPath specifies the D-Bus object path for the daemon.
	ObjectPath settings.String `default:"/com/example/MyService1" mutation:"once"`

	// ConnectionTimeout specifies the duration to wait for D-Bus connection establishment.
	// Defaults to 10 seconds.
	ConnectionTimeout settings.Duration `default:"10s"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	return bp, nil
}

func (s *Settings) Defaults() {}

// DeriveDBusNames converts an identifier into a D-Bus service name and object path.
// For example, "com.mycompany.myproject.cmd.myproject" becomes:
// - ServiceName: "com.mycompany.Myproject"
// - ObjectPath: "/com/mycompany/Myproject"
// If the identifier lacks ".cmd.", the last component is title-cased using Unicode-aware rules.
func DeriveDBusNames(identifier string) (serviceName, objectPath string) {
	c := cases.Title(language.English)

	base, cmd, found := strings.Cut(identifier, ".cmd.")
	if !found {
		base = identifier
		cmd = ""
	}

	parts := strings.Split(base, ".")
	if len(parts) == 0 {
		// Empty or malformed; use cmd or identifier
		if cmd != "" {
			serviceName = c.String(cmd)
		} else {
			serviceName = c.String(identifier)
		}
	} else {
		// Use cmd as last part if available, else last part of base
		last := parts[len(parts)-1]
		if cmd != "" {
			last = cmd
		}
		// Title-case the last part
		parts[len(parts)-1] = c.String(last)
		serviceName = strings.Join(parts, ".")
	}

	// object path
	objectPath = "/" + strings.ReplaceAll(serviceName, ".", "/")
	return serviceName, objectPath
}
