// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package healthcheck

import (
	"sync"
	"time"

	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/sdk/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type HandlerFunc func(sess *session.Context, status *Status) error

type Status struct {
	mu sync.RWMutex
	hf HandlerFunc
}

func WithHandlerFunc(s *Status, f HandlerFunc) *Status {
	s.hf = f
	return s
}
func (s *Status) Snapshot(sess *session.Context) (*Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.hf != nil {
		if err := s.hf(sess, s); err != nil {
			return nil, err
		}
	}
	snapshot := &Snapshot{
		ts:    time.Now(),
		state: ipcpb.HealthStatusSnapshot_HEALTHY,
	}
	return snapshot, nil
}

type Snapshot struct {
	state ipcpb.HealthStatusSnapshot_HealthState
	ts    time.Time
}

func (s *Snapshot) IpcResponseMessage() *ipcpb.HealthStatusSnapshot {
	return &ipcpb.HealthStatusSnapshot{
		State:     s.state,
		Timestamp: timestamppb.New(s.ts),
	}
}

func ParseSnapshotIPC(hss *ipcpb.HealthStatusSnapshot) *Snapshot {
	return &Snapshot{
		state: hss.State,
		ts:    hss.Timestamp.AsTime(),
	}
}

// type Handler interface {
// 	Handle(sess *session.Context, status *Status) error
// }

// type MiddlewareFunc func(Handler) Handler
