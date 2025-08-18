// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/session"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Peer struct {
	mu                sync.RWMutex
	pid               atomic.Int64
	id                uuid.UUID
	conn              net.Conn
	sess              *session.Context
	key               cipher.Block
	heartbeatInterval time.Duration
}

func NewPeer(sess *session.Context, conn net.Conn) (peer *Peer, err error) {
	peer = &Peer{
		id:                uuid.New(),
		conn:              conn,
		sess:              sess,
		heartbeatInterval: sess.Get("daemon.ipc.peer_heartbeat_interval").Duration(),
	}
	peer.pid.Store(int64(os.Getpid()))

	peer.debug(sess.Log(), "established connection")

	peer.key, err = NewCipher(sess.Get("daemon.ipc.encryption_key").String())
	if err != nil {
		return nil, err
	}

	if err := peer.handshake(); err != nil {
		defer func() {
			_ = peer.Disconnect()
		}()
		return nil, err
	}
	peer.ok(sess.Log(), "handshake successful")
	return

}

func (peer *Peer) NextFrame(ctx context.Context, deadline time.Time) (*Frame, error) {

	peer.mu.RLock()
	conn := peer.conn
	key := peer.key
	peer.mu.RUnlock()

	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetReadDeadline(deadline)

	// to make the read cancellable
	type result struct {
		frame *Frame
		err   error
	}

	resultCh := make(chan result, 1)

	go func() {
		defer close(resultCh)
		frame, err := NextFrame(conn, key)
		// Try to send result, but don't block if nobody's listening
		select {
		case resultCh <- result{frame, err}:
		case <-ctx.Done():
			// Context canceled while we were trying to send - just exit
			return
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res, ok := <-resultCh:
		if !ok {
			// Channel was closed, shouldn't happen in normal flow
			return nil, fmt.Errorf("%w: frame reader channel closed unexpectedly", ErrPeer)
		}

		if res.err != nil {
			if netErr, ok := res.err.(net.Error); ok && netErr.Timeout() {
				return nil, ErrPeerHeartbeatTimeout
			}
			if errors.Is(res.err, io.EOF) {
				return nil, ErrPeerDisconnected
			}
		}

		return res.frame, res.err
	}
}

func (p *Peer) handshake() error {

	p.mu.RLock()
	sess := p.sess
	sessionID := p.id
	p.mu.RUnlock()

	timeout := sess.Get("daemon.ipc.peer_handshake_timeout").Duration()
	p.debug(sess.Log(), "waiting for handshake")

	frame, err := p.NextFrame(sess, time.Now().Add(timeout))
	if err != nil {
		return err
	}

	if frame.raw.Metadata.Kind != ipcpb.Frame_HANDSHAKE {
		return fmt.Errorf("%w: got frame(%s) instead of a handshake frame", ErrPeer, frame.raw.Metadata.Kind.String())
	}

	req, err := frame.AsRequest()
	if err != nil {
		return err
	}

	hsreq := req.raw.GetHandshake()

	if hsreq == nil {
		defer func() {
			_ = p.Disconnect()
		}()
		return fmt.Errorf("%w: got request(%T) instead of a handshake request", ErrPeer, req.raw.Body)
	}

	statusMsg := "handshake successful"

	res := NewResponse(&ipcpb.Response{
		RequestId: &ipcpb.UUID{Value: uuid.New().String()},
		Type:      ipcpb.Response_HANDSHAKE,
		Status: &ipcpb.Response_Status{
			Type:    ipcpb.Response_Status_SUCCESS,
			Code:    200,
			Message: &statusMsg,
		},
		Timestamp: timestamppb.Now(),
		Body: &ipcpb.Response_Handshake{
			Handshake: &ipcpb.HandshakeResponse{
				SessionId:         &ipcpb.UUID{Value: sessionID.String()},
				ProtocolVersion:   ProtocolVersion,
				HeartbeatInterval: durationpb.New(p.sess.Get("daemon.ipc.peer_heartbeat_interval").Duration()),
			},
		},
	}, req)

	res.metadata.Kind = ipcpb.Frame_HANDSHAKE
	res.metadata.CorrelationId = req.metadata.CorrelationId

	resframe, err := res.frame(p.key)
	if err != nil {
		return err
	}

	if err := p.Send(resframe); err != nil {
		return err
	}

	p.ok(sess.Log(), "peer connected:",
		slog.String("id", sessionID.String()),
		slog.Uint64("protocol.version", uint64(hsreq.ProtocolVersion)),
		slog.String("handshake.took", resframe.raw.Metadata.ReqDur.AsDuration().String()),
	)

	return nil
}

func (p *Peer) ID() string {
	return p.id.String()
}

func (p *Peer) Send(frame *Frame) error {
	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()
	if _, err := frame.WriteTo(conn); err != nil {
		return err
	}
	return nil
}

func (p *Peer) Disconnect() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn == nil {
		return fmt.Errorf("%w: conn already closed", ErrPeerDisconnected)
	}

	err := p.conn.Close()
	p.conn = nil
	return err
}

func (p *Peer) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := p.pid.Load()
	args = append(args, slog.String("peer.id", p.id.String()))
	log(logger, logging.LevelDebug, "daemon-peer", pid, msg, args...)
}

func (p *Peer) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := p.pid.Load()
	args = append(args, slog.String("peer.id", p.id.String()))
	log(logger, logging.LevelInfo, "daemon-peer", pid, msg, args...)
}

func (p *Peer) ok(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := p.pid.Load()
	args = append(args, slog.String("peer.id", p.id.String()))
	log(logger, logging.LevelOk, "daemon-peer", pid, msg, args...)
}
