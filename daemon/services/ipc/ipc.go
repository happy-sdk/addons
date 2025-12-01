// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package ipc provides daemon's inter-process communication service.
package ipc

import (
	"fmt"

	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/pkg/settings"
)

const (
	ServiceName = "daemon-ipc"
	// EncryptionKeyLenght is AES-256 key len
	EncryptionKeyLenght int    = 32
	ProtocolVersion     uint32 = 1
)

var (
	Error = fmt.Errorf(ServiceName)
)

// Settings defines the configuration for the daemon's inter-process communication (IPC)
// service, managing timeouts and encryption for peer communication. It is used to customize
// IPC behavior, often integrated with D-Bus for system-level interactions.
type Settings struct {
	// PeerHandshakeTimeout specifies the duration to wait for a peer handshake to complete.
	// Defaults to 10 seconds.
	PeerHandshakeTimeout settings.Duration `default:"10s"`

	// PeerHeartbeatInterval specifies the maximum interval between heartbeat signals received
	// from a peer. If no heartbeat is received within this duration, the daemon drops the peer
	// connection. Defaults to 30 seconds.
	PeerHeartbeatInterval settings.Duration `default:"30s"`

	// EncryptionKey specifies the key used for encrypting IPC communications.
	EncryptionKey settings.String `key:"encryption_key,save" default:"7a1bde02132f49f8970e5cd6f9d18742", mutation:"once"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	bp.AddValidator("encryption_key", "validate the ipc encryption key", func(s settings.Setting) error {
		if s.Value().Empty() {
			return nil
		}
		if keyLen := len(s.String()); keyLen != EncryptionKeyLenght {
			return fmt.Errorf("%w: %s must be lenght of (%d) got (%d)", Error, s.Key(), EncryptionKeyLenght, keyLen)
		}
		return nil
	})
	return bp, nil
}

func (s *Settings) Defaults() {}

func Options() []*options.OptionSpec {
	return []*options.OptionSpec{
		options.NewOption("ipc.socket", ""),
	}
}

func UnmarshalIPC(data []byte) (*Settings, error) {
	return nil, nil
}

func MarshalIPC(s *Settings) ([]byte, error) {
	return nil, nil
}
