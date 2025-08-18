// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/happy-sdk/addons/daemon/healthcheck"
	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/session"
)

type Server struct {
	mu       sync.RWMutex
	pid      atomic.Int64
	sess     *session.Context
	listener net.Listener
	stats    ServerStats
	peers    map[string]*Peer
	status   *healthcheck.Status
}

func NewServer(sess *session.Context, status *healthcheck.Status) *Server {
	return &Server{
		sess:   sess,
		status: status,
	}
}

func (s *Server) Start() (err error) {
	s.pid.Store(int64(os.Getpid()))

	s.mu.Lock()
	var lc net.ListenConfig
	lc.Control = func(network, address string, c syscall.RawConn) error {
		s.debug(s.sess.Log(),
			"net.ListenConfig.Control not implemented",
			slog.String("network", network),
			slog.String("address", address),
		)
		return nil
	}

	s.stats.StartedAt = time.Now()
	s.listener, err = lc.Listen(s.sess, "unix", s.sess.Get("daemon.ctl.socket").String())
	if err != nil {
		return err
	}

	s.mu.Unlock()

	go s.listen()

	return nil
}

func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listener == nil {
		return nil
	}

	if err := s.listener.Close(); err != nil {
		return fmt.Errorf("failed to close ipc server: %s", err.Error())
	}
	s.info(s.sess.Log(), "ipc server closed")

	return nil
}

func (s *Server) listen() {
	s.mu.Lock()
	listener := s.listener
	sess := s.sess
	s.mu.Unlock()

	s.info(sess.Log(), "waiting for connections", slog.String("listening", sess.Get("daemon.ctl.socket").String()))

	for {
		conn, err := listener.Accept()
		if err != nil {
			if sess.Err() != nil {
				s.info(sess.Log(), "shutting down listener...")
				break
			}
			s.err(sess.Log(), "failed to accept connection:", slog.String("err", err.Error()))
			continue
		}

		go s.establish(sess, conn)
	}

	s.info(sess.Log(), "listener closed")
}

func (s *Server) establish(sess *session.Context, conn net.Conn) {

	s.stats.HandshakeAttempts.Add(1)

	peer, err := NewPeer(sess, conn)
	if err != nil {
		s.stats.HandshakesFailed.Add(1)
		s.err(sess.Log(), err.Error())
		return
	}

	s.attach(peer)

	defer func() {
		s.detach(peer)
	}()

	for {
		select {
		case <-sess.Done():
			return
		default:
			frame, err := peer.NextFrame(sess, time.Now().Add(peer.heartbeatInterval))
			if err != nil {
				if errors.Is(err, ErrPeerDisconnected) {
					s.info(sess.Log(), "peer disconnected")
					return
				}
				s.err(sess.Log(), err.Error())
				return
			}
			s.handle(peer, frame)
		}
	}
}

func (s *Server) attach(peer *Peer) {
	s.mu.Lock()
	if s.peers == nil {
		s.peers = make(map[string]*Peer)
	}
	s.peers[peer.ID()] = peer
	s.mu.Unlock()

	s.stats.PeersConnected.Add(1)
	s.stats.TotalConnections.Add(1)
}

func (s *Server) detach(peer *Peer) {
	s.mu.Lock()
	delete(s.peers, peer.ID())

	s.mu.Unlock()

	s.stats.PeersConnected.Add(-1)

	if err := peer.Disconnect(); err != nil {
		s.err(peer.sess.Log(), err.Error())
	} else {
		peer.info(peer.sess.Log(), "peer disconnected")
	}
}

func (s *Server) handle(peer *Peer, frame *Frame) {
	peer.debug(peer.sess.Log(), fmt.Sprintf("recieved frame: %s", frame.raw.Metadata.Kind.String()))

	switch frame.raw.Metadata.Kind {
	// respond to ping
	case ipcpb.Frame_PING:
		pong, err := NewPong(frame.raw.Metadata.GetSequence())
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
			return
		}
		pongframe, err := pong.frame(peer.key, frame.recvts)
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
			return
		}
		if err := peer.Send(pongframe); err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
	// handle requests
	case ipcpb.Frame_REQUEST:
		if req, err := frame.AsRequest(); err != nil {
			s.err(peer.sess.Log(), err.Error())
		} else {
			s.handleRequest(peer, req)
		}
	// invalid frame kind
	default:
		s.err(peer.sess.Log(), "invalid frame")
		req, err := frame.AsRequest()
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
		errorframe, err := NewRequestError(
			req,
			ipcpb.Response_Status_REJECTED,
			"Frame rejected",
			nil,
		).frame(peer.key)
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
		if err := peer.Send(errorframe); err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
	}
}

func (s *Server) handleRequest(peer *Peer, req *Request) {

	// Invalid session
	if req.raw.SessionId.Value != peer.ID() {
		s.err(peer.sess.Log(), "invalid session id")
		errorframe, err := NewRequestError(
			req,
			ipcpb.Response_Status_UNAUTHORIZED,
			"Unauthorized",
			nil,
		).frame(peer.key)
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
		}

		if err := peer.Send(errorframe); err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
		return
	}

	// Request with Request_Command Body
	if cmd := req.raw.GetCommand(); cmd != nil {
		s.handleCommand(peer, req, cmd)
		return
	}

	s.err(peer.sess.Log(), "unknown request type")
	errorframe, err := NewRequestError(
		req,
		ipcpb.Response_Status_REJECTED,
		"Unknown request type",
		nil,
	).frame(peer.key)
	if err != nil {
		s.err(peer.sess.Log(), err.Error())
	}
	if err := peer.Send(errorframe); err != nil {
		s.err(peer.sess.Log(), err.Error())
	}
}

func (s *Server) handleCommand(peer *Peer, req *Request, cmd *ipcpb.CommandRequest) {
	switch cmd.GetType() {
	case ipcpb.CommandRequest_HEALTHCHECK:
		s.mu.RLock()
		status := s.status
		s.mu.RUnlock()
		snapshot, err := status.Snapshot(peer.sess)
		// Snapshot failed
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
			errframe, err := NewRequestError(
				req,
				ipcpb.Response_Status_UNAUTHORIZED,
				"Health check failed",
				&ipcpb.Response_Details{
					Description: "Snapshot error",
					Context: map[string]string{
						"error": err.Error(),
					},
				},
			).frame(peer.key)
			if err != nil {
				s.err(peer.sess.Log(), err.Error())
			}
			if err := peer.Send(errframe); err != nil {
				s.err(peer.sess.Log(), err.Error())
			}
			return
		}

		res := NewResponse(&ipcpb.Response{
			Type: ipcpb.Response_COMMAND,
			Body: &ipcpb.Response_Health{
				Health: snapshot.IpcResponseMessage(),
			},
		}, req)
		frame, err := res.frame(peer.key)
		if err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
		if err := peer.Send(frame); err != nil {
			s.err(peer.sess.Log(), err.Error())
		}
	}
}

func (s *Server) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	log(logger, logging.LevelDebug, "daemon-ipc-server", pid, msg, args...)
}

func (s *Server) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	log(logger, logging.LevelInfo, "daemon-ipc-server", pid, msg, args...)
}

// func (s *Server) warn(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := s.pid.Load()
// 	log(logger, logging.LevelWarn, "daemon-ipc-server", pid, msg, args...)
// }

func (s *Server) err(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := s.pid.Load()
	log(logger, logging.LevelError, "daemon-ipc-server", pid, msg, args...)
}

func log(logger logging.Logger, lvl logging.Level, svc string, pid int64, msg string, args ...slog.Attr) {
	args = append(args, slog.Int64("pid", pid))
	logger.LogDepth(2, lvl, fmt.Sprintf("%s: %s", svc, msg), args...)
}

type ServerStats struct {
	StartedAt         time.Time
	HandshakeAttempts atomic.Uint64
	HandshakesFailed  atomic.Uint64

	PeersConnected   atomic.Int64 // counter for currently active connections
	TotalConnections atomic.Int64 // counter for total connections

	// RequestsTotal          atomic.Uint64
	// RequestsFailed         atomic.Uint64
	// ResponseTimeAverage    time.Duration
	// ResponseTimeSlowest    time.Duration
	// ResponseHandlerSlowest string
}

func (ds *ServerStats) Uptime() time.Duration {
	return time.Since(ds.StartedAt)
}
