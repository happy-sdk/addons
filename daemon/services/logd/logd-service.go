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
	"strconv"
	"strings"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils"
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
		logDirName := sess.Settings().Get("daemon.log.dir_name").String()

		logDir := filepath.Join(
			sess.Get("app.fs.path.profile.logs").String(),
			logDirName)

		if err := sess.Opts().Set("daemon.log.dir", logDir); err != nil {
			return fmt.Errorf("failed to set daemon log directory: %w", err)
		}

		if err := os.MkdirAll(logDir, 0750); err != nil {
			l.error(sess.Log(), err.Error())
			return fmt.Errorf("%w: failed to create log directory", Error)
		}

		// Daemon log file
		if err := sess.Opts().Set("daemon.log.file", filepath.Join(
			logDir,
			sess.Settings().Get("daemon.log.file_name").String(),
		)); err != nil {
			return fmt.Errorf("failed to set daemon log file path: %w", err)
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

		// Setup cron
		rotationInterval := sess.Settings().Get("daemon.log.rotation_interval").String()

		maxsize, err := bytesize.Parse(sess.Settings().Get("daemon.log.max_file_size").Value().String())
		if err != nil {
			return fmt.Errorf("%w: failed to parse max log file size setting", Error)
		}

		svc.Cron(func(schedule services.CronScheduler) {
			schedule.Job("scheduled log rotation", rotationInterval, l.rotationJob)

			if maxsize.Bytes() > 0 {
				schedule.Job("log rotation due excessive log file size", "@every 1m", l.rotationDueSizeJob)
			} else {
				sess.Log().Warn("max log file size is not set")
			}

			schedule.Job("log telemetry update", "@every 2s", func(sess *session.Context) error {
				l.mu.RLock()
				state := l.state
				atel := l.atel
				l.mu.RUnlock()

				state.UpdateLogger(func(ls *telemetry.LoggerState) {
					ls.Notices = atel.Notices.Load()
					ls.NotImplemented = atel.NotImplemented.Load()
					ls.Warnings = atel.Warnings.Load()
					ls.Deprecations = atel.Deprecations.Load()
					ls.Errors = atel.Errors.Load()
					ls.Others = atel.Others.Load()
					ls.Total = atel.Total.Load()
				})

				sess.Log().Notice("log telemetry update")
				fmt.Println("log telemetry update")

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
		l.debug(sess.Log(), "logger service disabled")
		return nil
	}

	state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Service.Status = telemetry.ServiceStatusStarting
	})

	rotationInterval := sess.Settings().Get("daemon.log.rotation_interval").String()
	cparser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	rotationSchedule, err := cparser.Parse(rotationInterval)
	if err != nil {
		return err
	}

	l.rotationStateFile = filepath.Join(sess.Opts().Get("daemon.log.dir").String(), "next.rotation")

	if !rotationSchedule.Disabled() {
		if err := l.setNextRotation(sess); err != nil {
			return err
		}
	}

	logfilePath := sess.Opts().Get("daemon.log.file").String()

	var ropts = []rotatefile.Option{
		rotatefile.RotatedFilePrefix("daemon_"),
	}
	if !sess.Settings().Get("daemon.log.startup_rotation_disabled").Value().Bool() {
		ropts = append(ropts, rotatefile.RotateOnOpen())
	}

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
			if pmStatus == telemetry.ServiceStatusStopped ||
				pmStatus == telemetry.ServiceStatusStopping {
				break wait
			}
		case <-ctx.Done():
			l.error(sess.Log(), "logger service stopped due to timeout")
			break wait
		}
	}

	// remove rotation state file
	if l.rotationStateFile != "" {
		if err := os.Remove(l.rotationStateFile); err != nil {
			l.warn(sess.Log(), fmt.Sprintf("failed to remove rotation state file: %v", err))
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

func (l *Logger) rotationJob(sess *session.Context) error {

	// hold lock so no other goroutine can access the file
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.setNextRotation(sess); err != nil {
		return err
	}

	rotationInterval := sess.Settings().Get("daemon.log.rotation_interval").String()
	l.debug(sess.Log(), "rotating log file scheduled", slog.String("scheduled", rotationInterval))

	file := l.file

	if err := file.Rotate(); err != nil {
		return err
	}

	// Give tail enough time to catch up
	time.Sleep(time.Millisecond * 10)

	l.info(sess.Log(), "scheduled log file rotation completed", slog.String("scheduled", rotationInterval))

	l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.Rotations = file.Rotations()
	})

	if err := l.rotationJobPruneEmptyLogFiles(sess); err != nil {
		return err
	}

	if err := l.rotationJobArchive(sess); err != nil {
		return err
	}

	return nil
}

func (l *Logger) rotationJobPruneEmptyLogFiles(sess *session.Context) error {
	// Prune empty log files
	if sess.Settings().Get("daemon.log.keep_empty_logs").Value().Bool() {
		return nil
	}

	entries, err := os.ReadDir(sess.Opts().Get("daemon.log.dir").String())
	if err != nil {
		return fmt.Errorf("%w: failed to list log files to prune", Error)
	}
	var needsPruning []string
	logfileName := sess.Settings().Get("daemon.log.file_name").String()
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == logfileName {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			l.error(sess.Log(), err.Error())
		}
		if info.Size() == 0 {
			needsPruning = append(needsPruning, filepath.Join(sess.Opts().Get("daemon.log.dir").String(), entry.Name()))
		}
	}

	if len(needsPruning) > 0 {
		l.debug(sess.Log(), fmt.Sprintf("pruning %d empty log file(s)", len(needsPruning)))
		for _, name := range needsPruning {
			if err := os.Remove(name); err != nil {
				l.error(sess.Log(), err.Error())
			}
		}
		l.debug(sess.Log(), fmt.Sprintf("pruned %d empty log file(s)", len(needsPruning)))
	}
	return nil
}

func (l *Logger) rotationJobArchive(sess *session.Context) error {
	archiveAfter := sess.Settings().Get("daemon.log.archive_after").Value().Duration()
	if archiveAfter < 0 {
		return nil
	}

	logDir := sess.Opts().Get("daemon.log.dir").String()
	archiveDir := filepath.Join(logDir, "archive")
	lastDir := filepath.Join(logDir, "last")
	logfileName := sess.Settings().Get("daemon.log.file_name").String()
	rotationStateFile := "next.rotation"

	if err := os.MkdirAll(archiveDir, 0750); err != nil {
		return err
	}
	if err := os.MkdirAll(lastDir, 0750); err != nil {
		return err
	}

	files, err := os.ReadDir(logDir)
	if err != nil {
		return err
	}

	var archived int
	for _, entry := range files {
		if entry.IsDir() || entry.Name() == logfileName || entry.Name() == rotationStateFile {
			continue
		}
		abspath := filepath.Join(logDir, entry.Name())
		if stat, err := fsutils.Stat(abspath); err != nil {
			l.error(sess.Log(), err.Error())
			continue
		} else {
			if stat.Btime.Before(time.Now().Add(-archiveAfter)) {
				newpath := filepath.Join(lastDir, entry.Name())
				if err := os.Rename(abspath, newpath); err != nil {
					l.error(sess.Log(), err.Error())
				}
				archived++
			}
		}
	}

	if archived > 0 {
		l.debug(sess.Log(), fmt.Sprintf("moved %d log files to archive batch", archived))
	}

	oldest, newest, bspan, err := fsutils.DirBtimeSpan(lastDir, false)
	if err != nil {
		l.error(sess.Log(), err.Error())
		return fmt.Errorf("failed to stat log archive batch dir %s", err.Error())
	}
	shouldArchive := bspan >= sess.Settings().Get("daemon.log.archive_batch_period").Value().Duration()

	if !shouldArchive {
		return nil
	}

	oldestStr := oldest.Format("20060102")
	newestStr := newest.Format("20060102")
	archiveName := fmt.Sprintf("daemon_logs_%s", oldestStr)
	if oldestStr != newestStr {
		archiveName = fmt.Sprintf("daemon_logs_%s_%s", oldestStr, newestStr)
	}

	// Compressed archive
	if sess.Settings().Get("daemon.log.archive_compression_disabled").Value().Bool() {
		archivedDir := getArchivePath(archiveDir, archiveName, "")
		if err := os.Rename(lastDir, archivedDir); err != nil {
			return fmt.Errorf("%w: failed to archive logs %s", err, err.Error())
		}
		filec, _, err := fsutils.CountFilesAndDirs(archivedDir)
		if err != nil {
			return err
		}
		l.info(sess.Log(), "log files archived",
			slog.Int("file_count", filec),
			slog.Time("from", oldest),
			slog.Time("to", newest),
		)
		// create new last dir
		if err := os.MkdirAll(lastDir, 0750); err != nil {
			return err
		}
	} else {
		tarpath := getArchivePath(archiveDir, archiveName, ".tar.gz")
		if err := fsutils.CompressDir(lastDir, tarpath); err != nil {
			return fmt.Errorf("failed to compress log archive batch :%s", err.Error())
		}
		l.info(sess.Log(), "logs archived",
			slog.String("archive", filepath.Base(tarpath)),
			slog.Time("from", oldest),
			slog.Time("to", newest),
		)
		if err := os.RemoveAll(lastDir); err != nil {
			return fmt.Errorf("failed to remove log archive: %s", err.Error())
		}
	}

	archiveEntries, err := os.ReadDir(archiveDir)
	if err != nil {
		return err
	}

	archiveRetentionPeriod := sess.Settings().Get("daemon.log.archive_retention_period").Value().Duration()
	for _, entry := range archiveEntries {
		archiveAbs := filepath.Join(archiveDir, entry.Name())
		if stat, err := fsutils.Stat(archiveAbs); err != nil {
			l.warn(sess.Log(), "failed to stat log archive", slog.String("err", err.Error()))
		} else {
			if entry.IsDir() {
				// for dirs use change time instead birth time
				shouldRemove := stat.Ctime.Before(time.Now().Add(-archiveRetentionPeriod))
				if !shouldRemove {
					continue
				}
				if err := os.RemoveAll(archiveAbs); err != nil {
					l.warn(sess.Log(), "failed to delete old log archive", slog.String("err", err.Error()), slog.String("archive", filepath.Base(archiveAbs)))
					continue
				}
			} else {
				shouldRemove := stat.Btime.Before(time.Now().Add(-archiveRetentionPeriod))
				if !shouldRemove {
					continue
				}
				if err := os.Remove(archiveAbs); err != nil {
					l.warn(sess.Log(), "failed to delete old log archive", slog.String("err", err.Error()), slog.String("archive", filepath.Base(archiveAbs)))
					continue
				}
			}

			l.info(sess.Log(), "log archive deleted", slog.String("archive", filepath.Base(archiveAbs)))
		}
	}
	return nil
}

func getArchivePath(archiveDir, archiveName, ext string) string {
	archivePath := filepath.Join(archiveDir, archiveName+ext)
	if _, err := os.Stat(archivePath); err != nil {
		return archivePath
	}

	entries, err := os.ReadDir(archiveDir)
	if err != nil {
		return archivePath
	}

	maxSequence := 0

	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, archiveName+".") {
			suffix := strings.TrimPrefix(name, archiveName+".")
			seqstr := strings.TrimSuffix(suffix, ext)
			seq, err := strconv.Atoi(seqstr)
			if err == nil && seq > maxSequence {
				maxSequence = seq
			}
		}
	}

	return filepath.Join(archiveDir, fmt.Sprintf("%s.%d%s", archiveName, maxSequence+1, ext))
}

func (l *Logger) setNextRotation(sess *session.Context) error {
	rotationInterval := sess.Settings().Get("daemon.log.rotation_interval").String()
	cparser := cron.NewParser(
		cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)
	schedule, err := cparser.Parse(rotationInterval)
	if err != nil {
		return err
	}
	l.nextRotation = schedule.Next(time.Now())

	if l.rotationStateFile != "" {
		if err := os.WriteFile(l.rotationStateFile, []byte(l.nextRotation.Format(time.RFC3339)), 0640); err != nil {
			return err
		}
	}

	l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
		ls.NextRotation = schedule.Next(time.Now())
	})

	return nil
}

func (l *Logger) rotationDueSizeJob(sess *session.Context) error {

	l.mu.Lock()
	defer l.mu.Unlock()

	defer func() {
		l.state.UpdateLogger(func(ls *telemetry.LoggerState) {
			logdirPath := sess.Get("daemon.log.dir").String()
			dirsize, err := fsutils.DirSize(logdirPath)
			if err != nil {
				l.warn(sess.Log(), "failed to get log dir size", slog.String("err", err.Error()))
				return
			}
			ls.DirSize = bytesize.SISize(dirsize)
		})
	}()

	// prevent concurrent rotation
	if !l.nextRotation.IsZero() && time.Until(l.nextRotation) < time.Minute {
		sess.Log().Debug("log rotation due to size job skipped to prevent concurrent rotation")
		return nil
	}

	maxsize, err := bytesize.Parse(sess.Settings().Get("daemon.log.max_file_size").Value().String())
	if err != nil {
		return fmt.Errorf("%w: failed to parse max log file size setting", Error)
	}

	stat, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("%w: failed to stat log file", Error)
	}
	if maxsize.Bytes() > stat.Size {
		return nil
	}
	l.info(sess.Log(),
		fmt.Sprintf(
			"log rotation due to size: %s > suggested size %s",
			bytesize.IECSize(stat.Size).String(),
			maxsize.String(),
		), slog.Uint64("current_size", stat.Size), slog.Uint64("max_size", maxsize.Bytes()))

	return l.file.Rotate()
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
