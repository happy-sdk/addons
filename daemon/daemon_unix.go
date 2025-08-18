// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

//go:build linux || darwin

package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"

	"github.com/happy-sdk/happy/sdk/session"
)

// startSingleFork implements single-fork daemon creation for efficient process management.
// Creates a detached daemon that remains as child of the original parent process.
func (api *API) startSingleFork(sess *session.Context) error {
	api.mu.Lock()
	defer api.mu.Unlock()

	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer func() {
		_ = null.Close()
	}()

	logFilePath := sess.Get("daemon.log.file.path").String()
	logFile, err := openLogFile(logFilePath)
	if err != nil {
		api.err(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to open log file", Error)
	}
	defer func() {
		_ = logFile.Close()
	}()
	if err := logFile.Rotate(); err != nil {
		api.err(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to rotate log file", Error)
	}

	procAttr := &os.ProcAttr{
		Dir: sess.Get("daemon.wd").String(),
		Env: os.Environ(),
		Sys: &syscall.SysProcAttr{
			Setsid: true,
		},

		Files: []*os.File{
			null,
			logFile.file,
			logFile.file,
		},
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}

	args := api.buildForkArgs(sess, filepath.Base(executable))

	child, err := os.StartProcess(executable, args, procAttr)
	if err != nil {
		return err
	}

	dpid := child.Pid
	if err := child.Release(); err != nil {
		return fmt.Errorf("%w: failed to release child process", Error)
	}

	if err := sess.Opts().Set("daemon.running", true); err != nil {
		api.err(sess.Log(), err.Error())
	}
	if err := sess.Opts().Set("daemon.pid", dpid); err != nil {
		api.err(sess.Log(), err.Error())
	}

	api.info(sess.Log(), fmt.Sprintf("daemon launched with PID: %d", dpid))
	return nil
}

func (api *API) startDoubleFork(sess *session.Context) error {
	api.mu.Lock()
	defer api.mu.Unlock()

	ctlpid := api.pid.Load()
	defer api.pid.Store(ctlpid)

	// Get executable path
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	// First fork
	api.debug(sess.Log(), "creating launcher")

	firstpid, err := doubleForkLauncherCreate(sess)
	if err != nil {
		return err
	}
	api.pid.Store(int64(os.Getpid()))

	// Only Launcher process will have firstpid here
	if firstpid > 0 {
		api.debug(sess.Log(), fmt.Sprintf("SYS_WAIT4(%d) waiting launcher to double fork", firstpid))
		// Parent process: Wait for first child to exit
		var status syscall.WaitStatus
		wpid, err := syscall.Wait4(int(firstpid), &status, 0, nil)
		if err != nil {
			e := fmt.Errorf("%w: failed to wait for first child: %v", Error, err)
			api.err(sess.Log(), e.Error())
			return e
		}
		if es := status.ExitStatus(); es != 0 {
			return fmt.Errorf("launcher process exited(%d)", es)
		}

		api.info(sess.Log(),
			"launcher exited with success",
			slog.Int("wpid", int(wpid)),
		)
		return nil
	}

	// temporary fork
	args := api.buildForkArgs(sess, filepath.Base(executable))
	dpid, err := doubleForkLauncherRun(sess, executable, args)
	if err != nil {
		api.err(sess.Log(), err.Error())
		os.Exit(1)
	}
	api.debug(sess.Log(), fmt.Sprintf("daemon process created PID(%d)", dpid))
	os.Exit(0)
	return nil
}

func doubleForkLauncherCreate(sess *session.Context) (int, error) {
	firstpid, _, errno := syscall.Syscall(syscall.SYS_FORK, 0, 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("%w: first fork failed: %v", Error, errno)
	}
	return int(firstpid), nil
}

func doubleForkLauncherRun(sess *session.Context, executable string, args []string) (int, error) {
	// First child: Create new session
	if _, err := syscall.Setsid(); err != nil {
		return 0, fmt.Errorf("%w: setsid failed: %v", Error, err)
	}

	// Open /dev/null for stdin/stdout/stderr redirection
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = null.Close()
	}()

	// Open log file
	logFilePath := sess.Get("daemon.log.file.path").String()
	logFile, err := openLogFile(logFilePath)
	if err != nil {
		return 0, fmt.Errorf("%w: failed to open log file: %s", Error, err.Error())
	}
	defer func() {
		_ = logFile.Close()
	}()

	if err := logFile.Rotate(); err != nil {
		return 0, fmt.Errorf("%w: failed to rotate log file %s", Error, err.Error())
	}

	// Setup for ForkExec
	procAttr := &syscall.ProcAttr{
		Dir: sess.Get("daemon.wd").String(),
		Env: os.Environ(),
		Files: []uintptr{
			null.Fd(),         // stdin
			logFile.file.Fd(), // stdout
			logFile.file.Fd(), // stderr
		},
		Sys: &syscall.SysProcAttr{
			// Run as specific user/group
			// Credential: &syscall.Credential{Uid: 1000, Gid: 1000},
		},
	}

	// ForkExec (second fork and exec)
	dpid, err := syscall.ForkExec(executable, args, procAttr)
	if err != nil {
		return dpid, fmt.Errorf("%w: forkexec failed: %v", Error, err)
	}
	return dpid, nil
}
