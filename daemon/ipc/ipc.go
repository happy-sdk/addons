// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"errors"
	"fmt"

	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/session"
)

//go:generate protoc -I/usr/local/include --proto_path=. --go_out=./ipcpb/ --go_opt=paths=source_relative ipc.proto

const (
	// EncryptionKeyLenght is AES-256 key len
	EncryptionKeyLenght int    = 32
	ProtocolVersion     uint32 = 1
)

var (
	Error                 = errors.New("ipc")
	ErrClient             = fmt.Errorf("%w client", Error)
	ErrClientNotConnected = fmt.Errorf("%w: not connected", ErrClient)

	ErrResponse = fmt.Errorf("%w-response", Error)

	ErrHandshakeFailed = fmt.Errorf("%w: handshake failed", Error)

	ErrPeer                  = fmt.Errorf("%w peer", Error)
	ErrPeerHandshakeTimeout  = fmt.Errorf("%w: handshake timed out", ErrPeer)
	ErrPeerDisconnected      = fmt.Errorf("%w: peer disconnected", ErrPeer)
	ErrPeerSessionIdMismatch = fmt.Errorf("%w: session id mismatch", ErrPeer)
	ErrPeerHeartbeatTimeout  = fmt.Errorf("%w: heartbeat timed out", ErrPeer)
)

// Handler is server command handler
type Handler[DATA any] func(sess *session.Context, req *Request, data DATA) (*Response, error)

type Settings struct {
	// PeerHandshakeTimeout is time.Duration to wait for peer handshake
	PeerHandshakeTimeout  settings.Duration `default:"10s"`
	PeerHeartbeatInterval settings.Duration `default:"30s"`
	EncryptionKey         settings.String   `key:"encryption_key,save" default:"7a1bde02132f49f8970e5cd6f9d18742"`
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
