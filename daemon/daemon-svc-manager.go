// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/happy-sdk/happy/pkg/fsutils/pidfile"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/events"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

func (inst *Instance) managerService() *services.Service {
	svc := services.New(service.Config{
		Name:         "daemon-manager",
		Description:  "Daemon manager service",
		RetryOnError: true,
		MaxRetries:   3,
		RetryBackoff: settings.Duration(5 * time.Second),
	})

	svc.OnStart(func(sess *session.Context) error {
		if inst.IsRunning() || sess.Get("daemon.running").Bool() {
			return ErrAlreadyRunning
		}
		inst.managerDebug(sess.Log(), "starting...")

		if _, err := inst.pidfileCreate(sess); err != nil {
			return err
		}

		inst.managerServiceSignalHandler(sess)

		ipcLoader := services.NewLoader(sess, "daemon-ipc")
		<-ipcLoader.Load()
		if err := ipcLoader.Err(); err != nil {
			inst.managerErr(sess.Log(), err.Error())
			return fmt.Errorf("%w: failed to start IPC service", Error)
		}

		_ = sess.Opts().Set("daemon.running", true)

		if err := inst.managerStartWorker(sess); err != nil {
			return err
		}

		inst.managerOk(sess.Log(), "daemon started")
		return nil
	})

	svc.OnStop(func(sess *session.Context, err error) error {
		inst.managerDebug(sess.Log(), "stopping...")

		if err != nil {
			inst.managerErr(sess.Log(), err.Error())
		}
		if err := inst.managerStopWorker(sess); err != nil {
			return err
		}

		ipc, err := sess.ServiceInfo("daemon-ipc")
		if err != nil {
			inst.managerErr(sess.Log(), err.Error())
		} else if ipc.Running() {
			inst.managerDebug(sess.Log(), "waiting for ipc to stop")

			ictx, icancel := context.WithTimeout(context.Background(), sess.Get("daemon.timeout").Duration())
			defer icancel()

			itimer := time.NewTicker(time.Microsecond * 100)
			defer itimer.Stop()

		iwait:
			for {
				select {
				case <-itimer.C:
					if !ipc.Running() {
						inst.managerDebug(sess.Log(), "IPC Service stopped")
						break iwait
					}
				case <-ictx.Done():
					inst.managerDebug(sess.Log(), "IPC Service did not stop in time")
					if err := ictx.Err(); err != nil {
						inst.managerErr(sess.Log(), err.Error())
					}
					break iwait
				}
			}
		}

		if err := inst.pidfileRemove(sess); err != nil {
			return err
		}

		_ = sess.Opts().Set("daemon.running", false)
		inst.managerOk(sess.Log(), "daemon stopped")
		return nil
	})

	svc.OnEvent(stopEvent.Scope(), stopEvent.Key(), func(sess *session.Context, ev events.Event) error {
		inst.managerDebug(sess.Log(), "stopEvent")
		defer sess.Destroy(nil)
		return inst.managerStopWorker(sess)
	})

	svc.OnEvent(sighupEvent.Scope(), sighupEvent.Key(), func(sess *session.Context, ev events.Event) error {
		inst.managerDebug(sess.Log(), "sighupEvent")
		return inst.managerReloadWorker(sess)
	})

	svc.Cron(func(schedule services.CronScheduler) {
		schedule.Job("log.rotation", "@midnight", func(sess *session.Context) error {
			inst.managerInfo(sess.Log(), "log rotation at @midnight")

			logFilePath := sess.Get("daemon.log.file.path").String()
			logFile, err := openLogFile(logFilePath)
			if err != nil {
				inst.managerErr(sess.Log(), err.Error())
				return fmt.Errorf("%w: failed to open log file", Error)
			}
			defer func() {
				_ = logFile.Close()
			}()

			rotated, err := logFile.RotateIfNeeded()
			if err != nil {
				inst.managerErr(sess.Log(), err.Error())
				return fmt.Errorf("%w: failed to rotate log file", Error)
			}
			if rotated {
				inst.managerOk(sess.Log(), "log file rotated")
				fd := int(logFile.file.Fd())

				// Redirect stdout to new log file
				if err := syscall.Dup2(fd, int(os.Stdout.Fd())); err != nil {
					return fmt.Errorf("failed to redirect stdout: %w", err)
				}

				// Redirect stderr to new log file
				if err := syscall.Dup2(fd, int(os.Stderr.Fd())); err != nil {
					return fmt.Errorf("failed to redirect stderr: %w", err)
				}
			}

			return logFile.CleanupOldLogs(30)
		})
	})

	return svc
}

func (inst *Instance) managerReloadWorker(sess *session.Context) error {
	inst.managerDebug(sess.Log(), "daemon worker reload")

	sess.Dispatch(ReloadingEvent)
	defer sess.Dispatch(ReloadedEvent)

	inst.busy.Store(true)
	defer inst.busy.Store(false)

	if err := inst.managerStopWorker(sess); err != nil {
		return err
	}

	return inst.managerStartWorker(sess)
}

func (inst *Instance) managerStartWorker(sess *session.Context) error {
	inst.managerDebug(sess.Log(), "daemon start")
	if inst.running.Load() {
		inst.managerDebug(sess.Log(), "daemon already running")
		return nil
	}

	var svcsToLoad []string
	svcs := sess.Settings().Get("daemon.services").Value().Fields()
	svcsToLoad = append(svcsToLoad, svcs...)

	svcsToLoad = append(svcsToLoad, "daemon-worker")
	workerLoader := services.NewLoader(sess, svcsToLoad...)
	<-workerLoader.Load()
	if err := workerLoader.Err(); err != nil {
		inst.managerErr(sess.Log(), err.Error())
		return fmt.Errorf("%w: failed to start Worker service", Error)
	}

	return nil
}

func (inst *Instance) managerStopWorker(sess *session.Context) error {
	inst.managerDebug(sess.Log(), "manager request worker to stop")
	if !inst.running.Load() {
		inst.managerDebug(sess.Log(), "worker already stopped")
		return nil
	}

	svcs := sess.Settings().Get("daemon.services").Value().Fields()

	sess.Dispatch(services.StopEvent.Create("daemon-worker", nil))

	for _, svc := range svcs {
		sess.Dispatch(services.StopEvent.Create(svc, nil))
	}

	worker, err := sess.ServiceInfo("daemon-worker")
	if err != nil {
		inst.managerErr(sess.Log(), err.Error())
	} else if worker.Running() {
		inst.managerDebug(sess.Log(), "waiting for worker to stop")

		wctx, wcancel := context.WithTimeout(context.Background(), sess.Get("daemon.timeout").Duration())
		defer wcancel()

		wtimer := time.NewTicker(time.Millisecond)
		defer wtimer.Stop()

	wwait:
		for {
			select {
			case <-wtimer.C:
				if !inst.running.Load() {
					inst.managerDebug(sess.Log(), "worker stopped")
					break wwait
				}
			case <-wctx.Done():
				inst.managerDebug(sess.Log(), "worker did not stop in time")
				inst.managerErr(sess.Log(), err.Error())
				break wwait
			}
		}
	}

	return nil
}

func (inst *Instance) pidfileCreate(sess *session.Context) (pid int, err error) {
	pidfilePath := sess.Get("daemon.pidfile.path").String()

	pf, err := pidfile.New(pidfilePath, 0, 0640)
	if err != nil {
		return 0, fmt.Errorf("failed to create daemon pidfile: %w", err)
	}
	defer func() {
		_ = pf.Close()
	}()

	pid, err = pf.PID()
	if err != nil {
		return pid, fmt.Errorf("failed to read daemon pidfile: %w", err)
	}

	if err := sess.Opts().Set("daemon.pid", pid); err != nil {
		return pid, fmt.Errorf("failed to set daemon pid: %w", err)
	}
	inst.managerOk(sess.Log(), "pidfile created", slog.Int("pid", pid))

	if err := sess.Opts().Set("daemon.pid", pid); err != nil {
		return pid, fmt.Errorf("failed to set daemon pid: %w", err)
	}

	return pid, nil
}

func (inst *Instance) pidfileRemove(sess *session.Context) error {
	pidfilePath := sess.Get("daemon.pidfile.path").String()
	pf, err := pidfile.Open(pidfilePath)
	if err != nil {
		return fmt.Errorf("failed to open pidfile: %w", err)
	}

	pid, _ := pf.PID()
	if err := pf.Remove(); err != nil {
		return fmt.Errorf("failed to remove pidfile: %w", err)
	}
	inst.managerOk(sess.Log(), "pidfile removed", slog.Int("pid", pid))
	return nil
}

func (inst *Instance) managerServiceSignalHandler(sess *session.Context) {
	signal.Reset()
	sess.Release()
	inst.managerDebug(sess.Log(), "relay incoming system signals to daemon-manager")

	// Create a channel to receive signals
	sigChan := make(chan os.Signal, 1)

	// Notify for all signals
	signal.Notify(sigChan)

	go func() {
		defer func() {
			signal.Stop(sigChan)
			close(sigChan)
		}()

		for {
			select {
			case <-sess.Done():
				return
			case sig := <-sigChan:

				// Wait busy state to complete
				if inst.busy.Load() {
					wait := time.NewTicker(time.Millisecond)

					for range wait.C {
						if !inst.busy.Load() {
							wait.Stop()
							break
						}
					}
				}

				switch sig {
				case syscall.SIGURG, syscall.SIGWINCH:
					break
				case syscall.SIGABRT:
					inst.managerNotice(sess.Log(), "received SIGABRT", slog.String("signal", sig.String()))
					sess.Dispatch(stopEvent)
					return
				case syscall.SIGTERM:
					inst.managerNotice(sess.Log(), "received SIGTERM", slog.String("signal", sig.String()))
					sess.Dispatch(stopEvent)
					return
				case syscall.SIGINT:
					inst.managerNotice(sess.Log(), "received SIGINT", slog.String("signal", sig.String()))
					sess.Dispatch(stopEvent)
					return
				case syscall.SIGQUIT:
					inst.managerNotice(sess.Log(), "received SIGQUIT", slog.String("signal", sig.String()))
					sess.Dispatch(stopEvent)
					return
				case syscall.SIGHUP:
					inst.managerNotice(sess.Log(), "received SIGHUP", slog.String("signal", sig.String()))
					sess.Dispatch(stopEvent)
				default:
					inst.managerWarn(sess.Log(), "received unhandled signal", slog.String("signal", sig.String()))
				}
			}
		}
	}()
}

func (inst *Instance) managerDebug(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelDebug, "daemon-manager", pid, msg, args...)
}

func (inst *Instance) managerInfo(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelInfo, "daemon-manager", pid, msg, args...)
}

func (inst *Instance) managerOk(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelOk, "daemon-manager", pid, msg, args...)
}

func (inst *Instance) managerNotice(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelNotice, "daemon-manager", pid, msg, args...)
}

func (inst *Instance) managerWarn(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelWarn, "daemon-manager", pid, msg, args...)
}

func (inst *Instance) managerErr(logger logging.Logger, msg string, args ...slog.Attr) {
	pid := inst.pid.Load()
	log(logger, logging.LevelError, "daemon-manager", pid, msg, args...)
}
