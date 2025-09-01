// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package telemetry

import (
	"sync"
	"time"

	"github.com/happy-sdk/happy/pkg/bytesize"
)

type ServiceStatus int8

const (
	ServiceStatusUnknown ServiceStatus = iota
	ServiceStatusDisabled
	ServiceStatusIdle
	ServiceStatusStarting
	ServiceStatusRestarting
	ServiceStatusRunning
	ServiceStatusStopping
	ServiceStatusStopped
	ServiceStatusFailed
)

type ServiceState struct {
	Name        string
	Addr        string
	Errors      map[time.Time]error
	StartedAt   time.Time
	StartUpTook time.Duration
	StoppedAt   time.Time
	Status      ServiceStatus
}

// DaemonState holds global daemon state.
type DaemonState struct {
	mu             sync.RWMutex
	processManager ProcessManagerState
	logger         LoggerState
}

// NewDaemonState initializes a new DaemonState.
func NewDaemonState() *DaemonState {
	return &DaemonState{}
}

// ProcessManagerState holds process-manager state and telemetry.
type ProcessManagerState struct {
	PID       int
	UpdatedAt time.Time
	Service   ServiceState
	Busy      bool
}

func (s *ProcessManagerState) AddError(err error) {
	if s.Service.Errors == nil {
		s.Service.Errors = make(map[time.Time]error)
	}
	s.Service.Errors[time.Now()] = err
}

// UpdateProcessManager applies f to the process-manager state under write lock.
func (s *DaemonState) UpdateProcessManager(f func(*ProcessManagerState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.processManager)
	s.processManager.UpdatedAt = time.Now()
}

// ProcessManager returns a copy of the state under read lock.
func (s *DaemonState) ProcessManager() ProcessManagerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.processManager
}

type LoggerState struct {
	PID          int
	UpdatedAt    time.Time
	Service      ServiceState
	NextRotation time.Time
	Rotations    int
	DirSize      bytesize.SISize

	LevelHappy          uint64
	LevelHappyInit      uint64
	LevelDebug          uint64
	LevelInfo           uint64
	LevelOk             uint64
	LevelNotice         uint64
	LevelNotImplemented uint64
	LevelWarn           uint64
	LevelDeprecated     uint64
	LevelError          uint64
	LevelBUG            uint64
	LevelAlways         uint64
	Total               uint64
}

func (s *LoggerState) AddError(err error) {
	if s.Service.Errors == nil {
		s.Service.Errors = make(map[time.Time]error)
	}
	s.Service.Errors[time.Now()] = err
}

// UpdateLogger applies f to the Logger state under write lock.
func (s *DaemonState) UpdateLogger(f func(*LoggerState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.logger)
	s.logger.UpdatedAt = time.Now()
}

// Logger returns a copy of the Logger state under read lock.
func (s *DaemonState) Logger() LoggerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logger
}
