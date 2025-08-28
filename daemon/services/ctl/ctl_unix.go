// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

//go:build linux || darwin

package ctl

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/session"
)

func startSingleFork(sess *session.Context, args action.Args) error {
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("%w: failed to open /dev/null", Error)
	}
	defer func() { _ = null.Close() }()

	var (
		stdout, stderr *os.File = null, null
	)

	logFileName := sess.Settings().Get("daemon.log.output_file_name").String()
	if !sess.Settings().Get("daemon.log.disabled").Value().Bool() &&
		logFileName != "" &&
		logFileName != "-" {

		outputLogDir := sess.Settings().Get("daemon.fs.path.logs").String()
		outputLogFilePath := filepath.Join(
			outputLogDir,
			logFileName,
		)
		outputLastDir := filepath.Join(outputLogDir, logd.OutputArchiveBatchDirName)
		cleanName := strings.TrimSuffix(logFileName, filepath.Ext(logFileName)) + "_"
		lfile, err := rotatefile.Open(
			outputLogFilePath,
			rotatefile.RotateOnOpen(),
			rotatefile.RotatedFilePrefix(cleanName),
			rotatefile.ArchiveDir(outputLastDir, rotatefile.DefaultArchivePerm),
		)
		if err != nil {
			return err
		}
		defer func() { _ = lfile.Close() }()
		fd, err := lfile.OpenFile(os.O_WRONLY | os.O_APPEND)
		if err != nil {
			return err
		}
		stdout, stderr = fd, fd
	}

	procAttr := &os.ProcAttr{
		Dir: sess.Settings().Get("daemon.fs.path.wd").String(),
		Env: os.Environ(),
		Sys: &syscall.SysProcAttr{
			Setsid:     true,
			Foreground: false,
			Pgid:       0,
		},
		Files: []*os.File{null, stdout, stderr},
	}

	executable, err := os.Executable()
	if err != nil {
		return err
	}

	forkArgs := buildForkArgs(sess, filepath.Base(executable), args)

	child, err := os.StartProcess(executable, forkArgs, procAttr)
	if err != nil {
		return err
	}

	dpid := child.Pid
	if err := child.Release(); err != nil {
		return fmt.Errorf("%w: failed to release child process", Error)
	}

	if err := sess.Opts().Set("daemon.process.running", true); err != nil {
		logError(sess.Log(), err.Error())
	}

	if err := sess.Opts().Set("daemon.process.pid", dpid); err != nil {
		logError(sess.Log(), err.Error())
	}

	logInfo(sess.Log(), fmt.Sprintf("daemon launched with PID: %d", dpid))

	return nil
}

func startDoubleFork(sess *session.Context, args action.Args) error {

	// Get executable path
	executable, err := os.Executable()
	if err != nil {
		return err
	}

	// First fork
	logDebug(sess.Log(), "creating launcher")

	firstpid, err := doubleForkLauncherCreate(sess)
	if err != nil {
		return err
	}

	// Only Launcher process will have firstpid here
	if firstpid > 0 {
		logDebug(sess.Log(), fmt.Sprintf("SYS_WAIT4(%d) waiting launcher to double fork", firstpid))
		// Parent process: Wait for first child to exit
		var status syscall.WaitStatus
		wpid, err := syscall.Wait4(int(firstpid), &status, 0, nil)
		if err != nil {
			e := fmt.Errorf("%w: failed to wait for first child: %v", Error, err)
			logError(sess.Log(), e.Error())
			return e
		}
		if es := status.ExitStatus(); es != 0 {
			return fmt.Errorf("launcher process exited(%d)", es)
		}

		logInfo(sess.Log(),
			"launcher exited with success",
			slog.Int("pid", int(wpid)),
		)
		return nil
	}

	// temporary fork
	forkArgs := buildForkArgs(sess, filepath.Base(executable), args)
	dpid, err := doubleForkLauncherRun(sess, executable, forkArgs)
	if err != nil {
		logError(sess.Log(), err.Error())
		os.Exit(1)
	}
	logDebug(sess.Log(), fmt.Sprintf("daemon process created PID(%d)", dpid))
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

	var (
		stdout, stderr *os.File = null, null
	)

	logFileName := sess.Settings().Get("daemon.log.output_file_name").String()
	if !sess.Settings().Get("daemon.log.disabled").Value().Bool() &&
		logFileName != "" &&
		logFileName != "-" {

		outputLogDir := sess.Settings().Get("daemon.fs.path.logs").String()
		outputLogFilePath := filepath.Join(
			outputLogDir,
			logFileName,
		)
		outputLastDir := filepath.Join(outputLogDir, logd.OutputArchiveBatchDirName)
		cleanName := strings.TrimSuffix(logFileName, filepath.Ext(logFileName)) + "_"
		lfile, err := rotatefile.Open(
			outputLogFilePath,
			rotatefile.RotateOnOpen(),
			rotatefile.RotatedFilePrefix(cleanName),
			rotatefile.ArchiveDir(outputLastDir, rotatefile.DefaultArchivePerm),
		)
		if err != nil {
			return 0, err
		}
		defer func() { _ = lfile.Close() }()
		fd, err := lfile.OpenFile(os.O_WRONLY | os.O_APPEND)
		if err != nil {
			return 0, err
		}
		stdout, stderr = fd, fd
	}

	// Setup for ForkExec
	procAttr := &syscall.ProcAttr{
		Dir: sess.Get("daemon.wd").String(),
		Env: os.Environ(),
		Files: []uintptr{
			null.Fd(),
			stdout.Fd(),
			stderr.Fd(),
		},
		Sys: &syscall.SysProcAttr{
			Setsid:     true,
			Foreground: false,
			Pgid:       0,
		},
	}

	// ForkExec (second fork and exec)
	dpid, err := syscall.ForkExec(executable, args, procAttr)
	if err != nil {
		return dpid, fmt.Errorf("%w: forkexec failed: %v", Error, err)
	}
	return dpid, nil
}
