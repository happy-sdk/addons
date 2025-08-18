// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package ipc

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/happy-sdk/addons/daemon/healthcheck"
	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

type Service struct {
	mu      sync.RWMutex
	pid     atomic.Int64
	running atomic.Bool
	server  *Server
}

func NewService() *Service {
	ipc := &Service{}
	ipc.pid.Store(int64(os.Getpid()))
	return ipc
}

func (ipcsvc *Service) AsService(status *healthcheck.Status) *services.Service {
	svc := services.New(service.Config{
		Name:         "daemon-ipc",
		Description:  "Daemon Inter-Process communication service",
		RetryOnError: true,
		MaxRetries:   3,
		RetryBackoff: settings.Duration(5 * time.Second),
	})

	svc.OnStart(func(sess *session.Context) (err error) {
		if ipcsvc.IsRunning() {
			return fmt.Errorf("%w: IPC service is already running", Error)
		}

		ipcsvc.debug(sess.Log(), "starting...")

		ipcsvc.mu.Lock()
		ipcsvc.server = NewServer(sess, status)
		ipcsvc.mu.Unlock()

		ipcsvc.mu.RLock()
		defer ipcsvc.mu.RUnlock()

		if err := ipcsvc.server.Start(); err != nil {
			return err
		}

		ipcsvc.running.Store(true)
		ipcsvc.ok(sess.Log(), "service started")
		status.SetState(ipcpb.HealthStatusSnapshot_HEALTHY)
		return nil
	})

	svc.OnStop(func(sess *session.Context, err error) error {
		ipcsvc.debug(sess.Log(), "stopping...")

		status.SetState(ipcpb.HealthStatusSnapshot_DEGRADED)

		if err != nil {
			ipcsvc.err(sess.Log(), err.Error())
		}

		worker, err := sess.ServiceInfo("daemon-worker")
		if err != nil {
			ipcsvc.err(sess.Log(), err.Error())
		} else if worker.Running() {
			ipcsvc.debug(sess.Log(), "waiting for worker to stop")

			wctx, wcancel := context.WithTimeout(context.Background(), sess.Get("daemon.timeout").Duration())
			defer wcancel()

			wtimer := time.NewTicker(time.Microsecond * 100)
			defer wtimer.Stop()

		wwait:
			for {
				select {
				case <-wtimer.C:
					if !worker.Running() {
						ipcsvc.debug(sess.Log(), "worker stopped")
						break wwait
					}
				case <-wctx.Done():
					ipcsvc.debug(sess.Log(), "worker did not stop in time")
					ipcsvc.err(sess.Log(), wctx.Err().Error())
					break wwait
				}
			}
		}

		ipcsvc.mu.Lock()
		defer func() {
			ipcsvc.mu.Unlock()
			ipcsvc.running.Store(false)
		}()

		// close net listener
		if ipcsvc.server != nil {
			if err := ipcsvc.server.Stop(); err != nil {
				return err
			}
		}

		ipcsvc.ok(sess.Log(), "service stopped")
		status.SetState(ipcpb.HealthStatusSnapshot_STOPPED)
		return nil
	})

	return svc
}

// IsRunning returns true if the daemon ipc service is running.
func (ipcsvc *Service) IsRunning() bool {
	return ipcsvc.running.Load()
}

func (ipcsvc *Service) debug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := ipcsvc.pid.Load()
	log(logger, logging.LevelDebug, "daemon-ipc", pid, msg, args...)
}

// func (ipcsvc *Service) info(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := ipcsvc.pid.Load()
// 	log(logger, logging.LevelInfo, "daemon-ipc", pid, msg, args...)
// }

// func (ipcsvc *Service) warn(logger logging.Logger, msg string, args ...slog.Attr) {
// 	pid := ipcsvc.pid.Load()
// 	log(logger, logging.LevelWarn, "daemon-ipc", pid, msg, args...)
// }

func (ipcsvc *Service) ok(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := ipcsvc.pid.Load()
	log(logger, logging.LevelOk, "daemon-ipc", pid, msg, args...)
}

func (ipcsvc *Service) err(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := ipcsvc.pid.Load()
	log(logger, logging.LevelError, "daemon-ipc", pid, msg, args...)
}

// func (ipcsvc *ipcService) handle(sess *session.Context, conn net.Conn) {
// 	defer func() {
// 		conn.Close()
// 		ipcsvc.stats.ConnCountActive.Add(-1)
// 		ipcsvc.info(sess.Log(), "connection closed")
// 	}()

// 	for {
// 		// now := time.Now()

// 		var length int32
// 		if err := binary.Read(conn, binary.BigEndian, &length); err != nil {
// 			ipc.info(sess.Log(), "client disconnected:", slog.String("msg", err.Error()))
// 			return
// 		}

// 		// Read raw data
// 		ipc.stats.RequestsTotal.Add(1)
// 		encData := make([]byte, length)
// 		if _, err := conn.Read(encData); err != nil {
// 			ipc.stats.RequestsFailed.Add(1)
// 			ipc.err(sess.Log(), fmt.Sprint("failed to read message:", err.Error()))
// 			return
// 		}

// 		// Decrypt data

// 		// Unmarshal protobuf request
// 	}
// }
