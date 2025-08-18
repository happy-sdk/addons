// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/happy-sdk/addons/daemon/healthcheck"
	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/sdk/session"
)

type Client struct {
	mu             sync.RWMutex
	pid            atomic.Int64
	sess           *session.Context
	connected      bool
	key            cipher.Block
	conn           net.Conn
	requestTimeout time.Duration

	handshakeMetadata map[string]string
	heartbeatInterval time.Duration
	protocolVersion   uint32
	sessionId         uuid.UUID
	lastActivityTime  time.Time
}

// NewClient creates new client to communicate with daemon-ipc service service
func NewClient(sess *session.Context) (c *Client, err error) {
	c = &Client{
		sess:           sess,
		requestTimeout: time.Duration(time.Second * 30),
	}

	c.pid.Store(int64(os.Getpid()))

	c.key, err = NewCipher(sess.Get("daemon.ipc.encryption_key").String())
	if err != nil {
		return nil, err
	}

	socketAddr := sess.Get("daemon.ctl.socket").String()
	c.debug(sess.Log(), fmt.Sprintf("client connecting: %s", socketAddr))

	return
}

// Connected retruns true if client is connected to background service
func (c *Client) Connected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connected
}

func (c *Client) Connect() (err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}
	reconnect := time.NewTicker(time.Second)
	reconnectTimer := time.NewTimer(time.Second * 5)
	defer reconnect.Stop()
	defer reconnectTimer.Stop()

	socketAddr := c.sess.Get("daemon.ctl.socket").String()

reconnecting:
	for {
		select {
		case <-c.sess.Done():
			return nil
		case <-reconnectTimer.C:
			return fmt.Errorf("%w: connect timout reached", ErrClient)
		case <-reconnect.C:
			c.conn, err = net.DialTimeout("unix", socketAddr, time.Duration(time.Second*30))
			if err != nil {
				continue
			}
			reconnect.Stop()
			reconnectTimer.Stop()
			break reconnecting
		}
	}

	return c.handshake()
}

func (c *Client) Ping(prevPongSeq uint64) (pong *Pong, err error) {

	ping, err := NewPing(prevPongSeq)
	if err != nil {
		return nil, err
	}

	pingframe, err := ping.frame(c.key)
	if err != nil {
		return nil, err
	}

	pongframe, err := c.Send(pingframe)
	if err != nil {
		return nil, err
	}

	return &Pong{
		f:    pongframe,
		addr: c.conn.RemoteAddr().String(),
	}, nil
}

func (c *Client) HealthCheck() (*healthcheck.Snapshot, error) {

	c.debug(c.sess.Log(), "perform healthcheck")

	req := NewRequest(&ipcpb.Request{
		SessionId: &ipcpb.UUID{Value: c.sessionId.String()},
		Body: &ipcpb.Request_Command{
			Command: &ipcpb.CommandRequest{
				Type: ipcpb.CommandRequest_HEALTHCHECK,
			},
		},
	})

	res, err := c.Request(req)
	if err != nil {
		return nil, err
	}

	health := res.RAW().GetHealth()
	if health == nil {

		fmt.Println("res.Metadata().Kind", res.Metadata().Kind)
		fmt.Println("res.Metadata().Length", res.Metadata().Length)
		fmt.Println("res.Metadata().Timestamp", res.Metadata().Timestamp)
		fmt.Println("res.Metadata().ReqDur", res.Metadata().ReqDur)
		fmt.Println("res.Metadata().CorrelationId", res.Metadata().CorrelationId)
		fmt.Println("res.Metadata().Sequence", res.Metadata().Sequence)

		fmt.Println("res.RAW().RequestId", res.RAW().RequestId)
		fmt.Println("res.RAW().Type", res.RAW().Type)
		fmt.Println("res.RAW().Headers", res.RAW().Headers)
		fmt.Println("res.RAW().Timestamp", res.RAW().Timestamp)
		fmt.Println("res.RAW().Status.Type", res.RAW().Status.Type)
		fmt.Println("res.RAW().Status.Code", res.RAW().Status.Code)
		fmt.Println("res.RAW().Status.Message", res.RAW().Status.Message)
		fmt.Println("res.RAW().Status.Details", res.RAW().Status.Details)

		fmt.Println("health", nil)

		return nil, fmt.Errorf(
			"%w: healthcheck failed: %s(%d): %s",
			ErrClient,
			res.raw.Status.Type.String(),
			res.raw.Status.GetCode(),
			res.raw.Status.GetMessage(),
		)
	}

	if res.raw.Status.Type != ipcpb.Response_Status_SUCCESS ||
		health.State != ipcpb.HealthStatusSnapshot_HEALTHY {
		fmt.Println("res.Metadata().Kind", res.Metadata().Kind)
		fmt.Println("res.Metadata().Length", res.Metadata().Length)
		fmt.Println("res.Metadata().Timestamp", res.Metadata().Timestamp)
		fmt.Println("res.Metadata().ReqDur", res.Metadata().ReqDur)
		fmt.Println("res.Metadata().CorrelationId", res.Metadata().CorrelationId)
		fmt.Println("res.Metadata().Sequence", res.Metadata().Sequence)

		fmt.Println("res.RAW().RequestId", res.RAW().RequestId)
		fmt.Println("res.RAW().Type", res.RAW().Type)
		fmt.Println("res.RAW().Headers", res.RAW().Headers)
		fmt.Println("res.RAW().Timestamp", res.RAW().Timestamp)
		fmt.Println("res.RAW().Status.Type", res.RAW().Status.Type)
		fmt.Println("res.RAW().Status.Code", res.RAW().Status.Code)
		fmt.Println("res.RAW().Status.Message", res.RAW().Status.Message)
		fmt.Println("res.RAW().Status.Details", res.RAW().Status.Details)

		fmt.Println("health.State", health.State)
		fmt.Println("health.Timestamp", health.Timestamp)
		fmt.Println("health.Message", health.Message)
		fmt.Println("health.Components", health.Components)

		return nil, fmt.Errorf(
			"%w: healthcheck failed: %s(%d): %s",
			ErrClient,
			res.raw.Status.Type.String(),
			res.raw.Status.GetCode(),
			res.raw.Status.GetMessage(),
		)
	}

	return healthcheck.ParseSnapshotIPC(health), nil
}

func (c *Client) handshake() error {
	c.debug(c.sess.Log(), "perform handshake")

	raw := &ipcpb.Request{
		Body: &ipcpb.Request_Handshake{
			Handshake: &ipcpb.HandshakeRequest{
				Metadata:        c.handshakeMetadata,
				ProtocolVersion: ProtocolVersion,
			},
		},
	}

	req := NewRequest(raw)

	res, err := c.request(req)
	if err != nil {
		return fmt.Errorf("%w: error sending request: %s", ErrHandshakeFailed, err.Error())
	}

	c.debug(c.sess.Log(), fmt.Sprintf("handshake took %s", res.metadata.ReqDur.AsDuration().String()))

	hs := res.raw.GetHandshake()
	if hs == nil {
		return fmt.Errorf("%w: invalid response body: %T", ErrHandshakeFailed, res.raw.Body)
	}
	c.heartbeatInterval = hs.HeartbeatInterval.AsDuration()
	c.protocolVersion = hs.ProtocolVersion
	c.sessionId, err = uuid.Parse(hs.SessionId.Value)
	if err != nil {
		return fmt.Errorf("%w: error parsing session id: %s", ErrHandshakeFailed, err.Error())
	}

	c.info(
		c.sess.Log(),
		"handshake completed",
		slog.String("session_id", hs.SessionId.Value),
		slog.Uint64("protocol_version", uint64(hs.ProtocolVersion)),
		slog.String("heartbeat_interval", hs.HeartbeatInterval.AsDuration().String()),
	)
	c.connected = true

	return nil
}

// Close client connection
func (c *Client) Close() (err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.conn == nil {
		return fmt.Errorf("%w: no conn to close", ErrClient)
	}
	c.connected = false
	err = c.conn.Close()
	c.conn = nil
	return
}

func (c *Client) Request(req *Request) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request(req)
}

func (c *Client) request(req *Request) (*Response, error) {
	c.lastActivityTime = time.Now()
	reqframe, err := req.frame(c.key)
	if err != nil {
		return nil, fmt.Errorf("%w: error framing request: %s", ErrHandshakeFailed, err.Error())
	}

	resframe, err := c.send(reqframe)
	if err != nil {
		return nil, err
	}
	return resframe.AsResponse()
}

func (c *Client) Send(frame *Frame) (*Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.send(frame)
}

func (c *Client) send(frame *Frame) (*Frame, error) {
	c.lastActivityTime = time.Now()

	if c.conn == nil {
		return nil, ErrClientNotConnected
	}
	if c.sess.Err() != nil {
		return nil, c.sess.Err()
	}

	c.debug(c.sess.Log(), fmt.Sprintf("sending frame: %s", frame.raw.Metadata.Kind.String()))

	_ = c.conn.SetDeadline(time.Now().Add(c.requestTimeout))

	err := binary.Write(c.conn, binary.BigEndian, frame.raw.MetadataLength)
	if err != nil {
		return nil, err
	}

	if _, err := c.conn.Write(frame.metadatab); err != nil {
		return nil, err
	}

	if len(frame.raw.Payload) > 0 {
		if _, err := c.conn.Write(frame.raw.Payload); err != nil {
			return nil, err
		}
	}

	resframe, err := NextFrame(c.conn, c.key)
	if err != nil {
		return nil, err
	}
	return resframe, nil
}

func (c *Client) ShouldHartbeat() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	adj := time.Second * 6
	return (time.Since(c.lastActivityTime) + adj) >= c.heartbeatInterval
}

func (c *Client) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := c.pid.Load()
	log(logger, logging.LevelDebug, "daemon-ipc-client", pid, msg, args...)
}

func (c *Client) info(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := c.pid.Load()
	log(logger, logging.LevelInfo, "daemon-ipc-client", pid, msg, args...)
}
