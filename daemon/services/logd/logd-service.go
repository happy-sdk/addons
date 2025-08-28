// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package logd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/services"
	"github.com/happy-sdk/happy/sdk/services/service"
	"github.com/happy-sdk/happy/sdk/session"
)

func (l *Logger) Service() *services.Service {
	svc := services.New(service.Config{
		Name:          ServiceName,
		Description:   "Daemon loging management service",
		RetryOnError:  false,
		MaxRetries:    0,
		RetryBackoff:  settings.Duration(5 * time.Second),
		LoaderTimeout: settings.Duration(time.Second * 5),
	})

	svc.OnRegister(l.onRegister(svc))
	svc.OnStart(l.onStart)
	svc.OnStop(l.onStop)

	return svc
}

func (l *Logger) onRegister(svc *services.Service) action.Action {
	return func(sess *session.Context) error {
		// Daemon log directory
		logDir := sess.Settings().Get("daemon.fs.path.logs").String()

		// Daemon log file
		if err := sess.Opts().Set("daemon.log.file", filepath.Join(
			logDir,
			sess.Settings().Get("daemon.log.log_file_name").String(),
		)); err != nil {
			return fmt.Errorf("failed to set daemon log file path: %w", err)
		}

		// Daemon log file
		if err := sess.Opts().Set("daemon.log.output", filepath.Join(
			logDir,
			sess.Settings().Get("daemon.log.output_file_name").String(),
		)); err != nil {
			return fmt.Errorf("failed to set daemon output file path: %w", err)
		}

		info, err := sess.ServiceInfo(ServiceName)
		if err != nil {
			return err
		}

		l.mu.RLock()
		defer l.mu.RUnlock()
		l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
			ls.Service.Name = info.Name()
			ls.Service.Addr = info.Addr().String()
			ls.Service.Errors = info.Errs()
			ls.Service.StartedAt = info.StartedAt()
			ls.Service.Status = telemetry.ServiceStatusStopped
		})

		logBatchDir := filepath.Join(
			sess.Settings().Get("daemon.fs.path.logs").String(),
			LogArchiveBatchDirName,
		)

		outBatchDir := filepath.Join(
			sess.Settings().Get("daemon.fs.path.logs").String(),
			OutputArchiveBatchDirName,
		)

		if err := os.MkdirAll(logBatchDir, 0750); err != nil {
			return fmt.Errorf("%w: failed to create batch directory %s: %s", Error, logBatchDir, err.Error())
		}
		if err := os.MkdirAll(outBatchDir, 0750); err != nil {
			return fmt.Errorf("%w: failed to create batch directory %s: %s", Error, outBatchDir, err.Error())
		}

		// Setup cron
		rotationScheduleExpr := sess.Settings().Get("daemon.log.rotation_schedule").String()
		rotationSchedule, err := cron.ParseWithOptionalSecond(rotationScheduleExpr)
		if err != nil {
			return err
		}

		archiveScheduleExp := sess.Settings().Get("daemon.log.archive_schedule").String()
		archiveSchedule, err := cron.ParseWithOptionalSecond(archiveScheduleExp)
		if err != nil {
			return err
		}

		maxsize, err := bytesize.Parse(sess.Settings().Get("daemon.log.max_file_size").Value().String())
		if err != nil {
			return fmt.Errorf("%w: failed to parse max log file size setting", Error)
		}
		// Check is stdout/stderr logging enabled
		outputLogEnabled := !sess.Settings().Get("daemon.log.log_file_name").Value().Empty() &&
			sess.Settings().Get("daemon.log.log_file_name").String() != "-"

		svc.Cron(func(schedule services.CronScheduler) {

			// Log rotation
			if !rotationSchedule.IsDisabled() {
				schedule.Job(
					"scheduled log rotation", rotationScheduleExpr, l.cronLogRotationSchedule)
				if outputLogEnabled {
					schedule.Job("scheduled output rotation", rotationScheduleExpr, l.cronOutputRotationSchedule)
				} else {
					l.info(sess.Log(), "output log rotation disabled")
				}
			} else {
				l.info(sess.Log(), "log rotation disabled")
			}

			// Log archive
			if !archiveSchedule.IsDisabled() {
				schedule.Job("scheduled log archive", archiveScheduleExp, l.cronLogArchiveSchedule)
				if outputLogEnabled {
					schedule.Job("scheduled output archive", archiveScheduleExp, l.cronOutputArchiveSchedule)
				} else {
					l.info(sess.Log(), "output logging archiving disabled")
				}
			} else {
				l.info(sess.Log(), "logging archiving disabled")
			}

			// Log size
			if maxsize.Bytes() > 0 {
				schedule.Job(
					"log rotation due excessive log file size",
					"@every 1m",
					l.cronLogExcessiveFileSize,
				)
				schedule.Job(
					"output log rotation due excessive log file size",
					"@every 1m",
					l.cronOutputExcessiveFileSize,
				)
			} else {
				l.warn(sess.Log(), "max log file size is not set")
			}

			// Log Telemetry
			schedule.Job("log telemetry update", "@every 2s", func(sess *session.Context) error {
				l.mu.RLock()
				state := l.state
				atel := l.atel
				l.mu.RUnlock()

				state.UpdateLogger(func(ls *telemetry.LoggerState) {
					ls.LevelHappy = atel.LevelHappy.Load()
					ls.LevelHappyInit = atel.LevelHappyInit.Load()
					ls.LevelDebug = atel.LevelDebug.Load()
					ls.LevelInfo = atel.LevelInfo.Load()
					ls.LevelOk = atel.LevelOk.Load()
					ls.LevelNotice = atel.LevelNotice.Load()
					ls.LevelWarn = atel.LevelWarn.Load()
					ls.LevelDeprecated = atel.LevelDeprecated.Load()
					ls.LevelError = atel.LevelError.Load()
					ls.LevelBUG = atel.LevelBUG.Load()
					ls.LevelAlways = atel.LevelAlways.Load()
					ls.Total = atel.Total.Load()
				})

				return nil
			})
		})

		l.debug(sess.Log(), "daemon-logd service registered")
		return nil
	}
}

func (l *Logger) onStart(sess *session.Context) (err error) {
	startedAt := time.Now()

	defer func() {
		l.handleStateDeferError(sess, err)
	}()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.state

	if sess.Settings().Get("daemon.log.disabled").Value().Bool() {
		state.UpdateLogger(func(ls *telemetry.LoggerState) {
			ls.Service.Status = telemetry.ServiceStatusDisabled
		})
		return fmt.Errorf("%w: logger service is disabled", Error)
	}

	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Service.Status = telemetry.ServiceStatusStarting
	})

	logfilePath := sess.Opts().Get("daemon.log.file").String()
	logDir := sess.Settings().Get("daemon.fs.path.logs").String()
	lastDir := filepath.Join(logDir, LogArchiveBatchDirName)

	logFileName := sess.Settings().Get("daemon.log.log_file_name").String()
	cleanName := strings.TrimSuffix(logFileName, filepath.Ext(logFileName)) + "_"

	var ropts = []rotatefile.Option{
		rotatefile.RotatedFilePrefix(cleanName),
		rotatefile.ArchiveDir(lastDir, 0),
	}

	if !sess.Settings().Get("daemon.log.startup_rotation_disabled").Value().Bool() {
		ropts = append(ropts, rotatefile.RotateOnOpen())
	}

	// Adapter takes ownership and closes the file when daemon exits
	l.file, err = rotatefile.Open(logfilePath, ropts...)
	if err != nil {
		return err
	}

	adapter, err := NewAdapter(sess, l.file, l.atel)
	if err != nil {
		return err
	}

	switch sess.Get("daemon.process.spawn_strategy").String() {
	case "foreground":
		if err := sess.Log().AttachAdapter(adapter); err != nil {
			return err
		}
	case "daemon":
		if err := sess.Log().SetAdapter(adapter); err != nil {
			return err
		}
	}

	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Service.Status = telemetry.ServiceStatusRunning
		ls.Service.StartUpTook = time.Since(startedAt)
	})

	took := state.Logger().Service.StartUpTook
	l.ok(
		sess.Log(),
		fmt.Sprintf("%s logger service started %s", sess.Get("app.slug"), took),
		slog.Duration("took", took),
	)

	return nil
}

func (l *Logger) onStop(sess *session.Context, err error) error {
	if sess.Settings().Get("daemon.log.disabled").Value().Bool() {
		return nil
	}
	defer func() {
		l.handleStateDeferError(sess, err)
	}()

	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.state

	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Service.Status = telemetry.ServiceStatusStopping
	})

	// ensure logger stops last
	ctx, cancel := context.WithTimeout(context.Background(), sess.Get("daemon.process.timeout").Duration())
	defer cancel()

	timer := time.NewTicker(time.Microsecond * 100)
	defer timer.Stop()

wait:
	for {
		select {
		case <-timer.C:
			pmStatus := state.ProcessManager().Service.Status
			if pmStatus == telemetry.ServiceStatusStopped {
				break wait
			}
		case <-ctx.Done():
			l.error(sess.Log(), "logger service stopped due to timeout")
			break wait
		}
	}

	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Service.Status = telemetry.ServiceStatusStopped
		ls.Service.StoppedAt = time.Now()
		ls.NextRotation = time.Time{}
	})

	l.ok(sess.Log(), fmt.Sprintf("%s daemon logger service stopped", sess.Get("app.slug")))
	return nil
}

func (l *Logger) handleStateDeferError(sess *session.Context, err error) {
	l.mu.RLock()
	state := l.state
	l.mu.RUnlock()
	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		info, infoErr := sess.ServiceInfo(ServiceName)
		if infoErr != nil {
			ls.AddError(infoErr)
			return
		}
		ls.Service.Errors = info.Errs()
		if err != nil {
			ls.AddError(err)
		}
		if len(ls.Service.Errors) > 0 {
			ls.Service.Status = telemetry.ServiceStatusFailed
		}
	})
}
