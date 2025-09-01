// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

//go:build linux || darwin

package ctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/fsutils/pidfile"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/session"
)

// startSingleFork launches daemon using single fork strategy
func startSingleFork(sess *session.Context, args action.Args) error {
	logDebug(sess.Log(), "creating single-fork process")

	output, err := setupOutputFile(sess)
	if err != nil {
		return err
	}
	defer output.Close()

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Open /dev/null for stdin
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("failed to open /dev/null: %w", err)
	}
	defer null.Close()

	procAttr := &os.ProcAttr{
		Dir:   sess.Settings().Get("daemon.fs.path.wd").String(),
		Env:   os.Environ(),
		Files: []*os.File{null, output, output},
		Sys: &syscall.SysProcAttr{
			Setsid:     true,
			Foreground: false,
			Pgid:       0,
		},
	}

	forkArgs := buildForkArgs(sess, filepath.Base(executable), args)

	child, err := os.StartProcess(executable, forkArgs, procAttr)
	if err != nil {
		return fmt.Errorf("failed to start daemon process: %w", err)
	}

	dpid := child.Pid
	if err := child.Release(); err != nil {
		return fmt.Errorf("%w: failed to release child process", Error)
	}

	if err := sess.Opts().Set("daemon.process.running", true); err != nil {
		logError(sess.Log(), fmt.Sprintf("failed to set running state: %v", err))
	}

	if err := sess.Opts().Set("daemon.process.pid", dpid); err != nil {
		logError(sess.Log(), fmt.Sprintf("failed to set PID: %v", err))
	}

	logInfo(sess.Log(), fmt.Sprintf("daemon launched with PID: %d", dpid))
	return nil
}

// startDoubleFork launches daemon using double fork strategy for full detachment
func startDoubleFork(sess *session.Context, args action.Args) error {
	logDebug(sess.Log(), "creating launcher process")

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	firstPid, err := doubleForkLauncherCreate(sess)
	if err != nil {
		return err
	}

	// Parent process - wait for launcher to complete
	if firstPid > 0 {
		logDebug(sess.Log(), fmt.Sprintf("waiting for launcher PID %d", firstPid))

		if err := waitForChild(firstPid, 30*time.Second); err != nil {
			return fmt.Errorf("launcher process failed: %w", err)
		}
		dpid, err := waitForPid(sess)
		if err != nil {
			return fmt.Errorf("failed to wait for daemon PID: %w", err)
		}

		logInfo(sess.Log(), fmt.Sprintf("daemon launched successfully via double fork, pid %d", dpid))

		if err := sess.Opts().Set("daemon.process.pid", dpid); err != nil {
			return fmt.Errorf("failed to set daemon pid: %w", err)
		}
		if err := sess.Opts().Set("daemon.process.running", dpid > 0); err != nil {
			return fmt.Errorf("failed to set daemon running status: %w", err)
		}

		return nil
	}

	// Launcher process - perform second fork and exec
	forkArgs := buildForkArgs(sess, filepath.Base(executable), args)

	dpid, err := doubleForkLauncherRun(sess, executable, forkArgs)
	if err != nil {
		logError(sess.Log(), fmt.Sprintf("failed to launch daemon: %v", err))
		os.Exit(1)
	}

	logInfo(sess.Log(), fmt.Sprintf("daemon process created with PID %d", dpid))
	os.Exit(0)

	return nil
}

// setupOutputFile configures stdout/stderr redirection for daemon
func setupOutputFile(sess *session.Context) (*os.File, error) {
	// If logging is disabled or no log file specified, use /dev/null
	outputFileName := sess.Settings().Get("daemon.log.output_file_name").String()
	if sess.Settings().Get("daemon.log.disabled").Value().Bool() ||
		outputFileName == "" ||
		outputFileName == "-" {

		null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("%w: failed to open /dev/null", Error)
		}
		return null, nil
	}

	// Setup log file with rotation
	outputLogDir := sess.Settings().Get("daemon.fs.path.logs").String()
	outputLogFilePath := filepath.Join(outputLogDir, outputFileName)
	outputArchiveDir := filepath.Join(outputLogDir, logd.OutputArchiveBatchDirName)
	cleanName := strings.TrimSuffix(outputFileName, filepath.Ext(outputFileName)) + "_"

	var ropts = []rotatefile.Option{
		rotatefile.RotatedFilePrefix(cleanName),
		rotatefile.ArchiveDir(outputArchiveDir, rotatefile.DefaultArchivePerm),
	}

	if !sess.Settings().Get("daemon.log.startup_rotation_disabled").Value().Bool() {
		ropts = append(ropts, rotatefile.RotateOnOpen())
	}

	lfile, err := rotatefile.Open(outputLogFilePath, ropts...)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}
	defer lfile.Close()

	fd, err := lfile.OpenFile(os.O_WRONLY | os.O_APPEND | os.O_CREATE)
	if err != nil {
		lfile.Close()
		return nil, fmt.Errorf("failed to open log file descriptor: %w", err)
	}

	return fd, nil
}

// doubleForkLauncherCreate performs the first fork
func doubleForkLauncherCreate(sess *session.Context) (int, error) {
	firstPid, _, errno := syscall.Syscall(syscall.SYS_FORK, 0, 0, 0)
	if errno != 0 {
		return 0, fmt.Errorf("%w: first fork failed: %v", Error, errno)
	}

	logDebug(sess.Log(), fmt.Sprintf("created launcher with PID %d", firstPid))
	return int(firstPid), nil
}

func doubleForkLauncherRun(sess *session.Context, executable string, args []string) (int, error) {
	// Create new session (detach from controlling terminal)
	if _, err := syscall.Setsid(); err != nil {
		return 0, fmt.Errorf("%w: setsid failed: %v", Error, err)
	}

	output, err := setupOutputFile(sess)
	if err != nil {
		return 0, err
	}
	defer output.Close()

	// Open /dev/null for stdin
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("failed to open /dev/null: %w", err)
	}
	defer null.Close()

	// Setup process attributes for second fork
	procAttr := &syscall.ProcAttr{
		Dir: sess.Settings().Get("daemon.fs.path.wd").String(),
		Env: os.Environ(),
		Files: []uintptr{
			null.Fd(),
			output.Fd(),
			output.Fd(),
		},
		Sys: &syscall.SysProcAttr{
			Setsid:     true,  // Create new session
			Foreground: false, // Run in background
			Pgid:       0,     // New process group
		},
	}

	// Perform second fork and exec
	dpid, err := syscall.ForkExec(executable, args, procAttr)
	if err != nil {
		return 0, fmt.Errorf("%w: forkexec failed: %v", Error, err)
	}

	return dpid, nil
}

// waitForChild waits for child process
func waitForChild(pid int, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		var status syscall.WaitStatus
		_, err := syscall.Wait4(pid, &status, 0, nil)
		if err != nil {
			done <- fmt.Errorf("wait4 failed: %w", err)
			return
		}
		if es := status.ExitStatus(); es != 0 {
			done <- fmt.Errorf("child process exited with status %d", es)
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timeout waiting for child process")
	}
}

// waitForPid waits for daemon to create pid file with timeout
func waitForPid(sess *session.Context) (int, error) {
	pidfilePath := sess.Opts().Get("daemon.process.pidfile").String()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return 0, fmt.Errorf("%w: timeout waiting for daemon to create pid file", Error)
		case <-ticker.C:
			pf, err := pidfile.Open(pidfilePath)
			if err != nil {
				// PidFile doesn't exist yet or can't be opened, continue waiting
				continue
			}

			dpid, err := pf.PID()
			if err != nil {
				pf.Close()
				continue
			}

			pf.Close()
			return dpid, nil
		}
	}
}
