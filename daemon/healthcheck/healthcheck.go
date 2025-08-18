// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package healthcheck

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/happy-sdk/addons/daemon/ipc/ipcpb"
	"github.com/happy-sdk/happy/pkg/strings/textfmt"
	"github.com/happy-sdk/happy/sdk/session"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type HandlerFunc func(sess *session.Context, status *Status) error

type Status struct {
	mu sync.RWMutex
	hf HandlerFunc

	state ipcpb.HealthStatusSnapshot_HealthState
}

func NewStatus() *Status {
	return &Status{
		state: ipcpb.HealthStatusSnapshot_UNKNOWN,
	}
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
		state: s.state,
	}
	return snapshot, nil
}

func (s *Status) SetState(state ipcpb.HealthStatusSnapshot_HealthState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = state
}

type Snapshot struct {
	ts    time.Time
	state ipcpb.HealthStatusSnapshot_HealthState
}

func NewSnapshot(err error) *Snapshot {
	snapshot := &Snapshot{
		ts:    time.Now(),
		state: ipcpb.HealthStatusSnapshot_UNKNOWN,
	}
	if err != nil {
		snapshot.state = ipcpb.HealthStatusSnapshot_UNHEALTHY
	}
	return snapshot
}

func (s *Snapshot) Timestamp() time.Time {
	return s.ts
}

func (s *Snapshot) State() ipcpb.HealthStatusSnapshot_HealthState {
	return s.state
}

func (s *Snapshot) TableString() string {
	tbl := textfmt.Table{
		Title: "HEALTHCHECK",
	}
	tbl.AddRow("TIMESTAMP", s.Timestamp().Format(time.RFC3339))
	tbl.AddRow("STATE", s.State().String())

	return tbl.String()
}

func (s *Snapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		State     string `json:"state"`
	}{
		Timestamp: s.Timestamp().Format(time.RFC3339),
		State:     s.State().String(),
	})
}

func ParseSnapshotIPC(hss *ipcpb.HealthStatusSnapshot) *Snapshot {
	return &Snapshot{
		state: hss.State,
		ts:    hss.Timestamp.AsTime(),
	}
}

func SnapshotToIPCMessage(s *Snapshot) *ipcpb.HealthStatusSnapshot {
	return &ipcpb.HealthStatusSnapshot{
		State:     s.state,
		Timestamp: timestamppb.New(s.ts),
	}
}
