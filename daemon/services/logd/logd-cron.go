// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package logd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/happy-sdk/addons/daemon/pkg/telemetry"
	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/sdk/session"
	"golang.org/x/sys/unix"
)

// LOG FILES
func (l *Logger) cronLogRotationSchedule(sess *session.Context) error {
	if !l.ready.Load() {
		l.debug(sess.Log(), "skip log rotation, too early")
		return nil
	}

	l.mu.RLock()
	logfile := l.file
	l.mu.RUnlock()

	if err := l.jobRotate(sess, logfile); err != nil {
		return fmt.Errorf("%w: failed to rotate file %s: %s", Error, logfile.Name(), err.Error())
	}

	batchDir := filepath.Join(
		sess.Settings().Get("daemon.fs.path.logs").String(),
		LogArchiveBatchDirName,
	)

	cleanName := strings.TrimSuffix(logfile.Name(), filepath.Ext(logfile.Name()))

	if err := l.jobCreateBatch(sess, batchDir, cleanName); err != nil {
		return fmt.Errorf("%w: failed to create log batch", err)
	}
	return nil
}

func (l *Logger) cronLogArchiveSchedule(sess *session.Context) error {
	batchDir := filepath.Join(
		sess.Settings().Get("daemon.fs.path.logs").String(),
		LogArchiveBatchDirName,
	)

	pruned, err := l.jobPruneEmptyFiles(sess, batchDir)
	if err != nil {
		l.warn(sess.Log(), "failed to prune empty log files", slog.String("err", err.Error()))
	} else if pruned > 0 {
		l.info(sess.Log(), "pruned empty log files", slog.Int("count", pruned))
	}

	logdir := sess.Settings().Get("daemon.fs.path.logs").String()
	archiveDir := filepath.Join(logdir, "archive", LogArchiveDirName)

	if _, err := os.Stat(batchDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	n, err := l.jobCreateArchive(sess, batchDir, archiveDir)
	if err != nil {
		return err
	}
	if n > 0 {
		l.info(sess.Log(), "archived log batches", slog.Int("count", n))
		l.statsUpdateLoggerFsStats(sess)
	}

	return l.jobCleanOldArchives(sess, archiveDir)
}

func (l *Logger) cronLogExcessiveFileSize(sess *session.Context) error {
	if !l.ready.Load() {
		l.debug(sess.Log(), "skip log size check, too early")
		return nil
	}
	l.mu.RLock()
	logfile := l.file
	l.mu.RUnlock()
	return l.jobExcessiveFileSize(sess, logfile)
}

// OUTPUT FILES
func (l *Logger) cronOutputRotationSchedule(sess *session.Context) error {

	if !l.ready.Load() {
		l.debug(sess.Log(), "skip output rotation, too early")
		return nil
	}

	outputFileName := sess.Settings().Get("daemon.log.output_file_name").String()
	if outputFileName == "" || outputFileName == "-" {
		return nil
	}

	outputLogDir := sess.Settings().Get("daemon.fs.path.logs").String()
	outputLogFilePath := filepath.Join(
		outputLogDir,
		outputFileName,
	)

	expectedInfo, err := os.Stat(outputLogFilePath)
	if err != nil {
		return fmt.Errorf("%w: failed to stat expected path", Error)
	}

	stdoutStat, err := os.Stdout.Stat()
	if err != nil {
		return fmt.Errorf("%w: failed to stat stdout", Error)
	}

	stderrStat, err := os.Stderr.Stat()
	if err != nil {
		return fmt.Errorf("%w: failed to stat stderr", Error)
	}

	isFd12 := os.SameFile(expectedInfo, stdoutStat) || os.SameFile(expectedInfo, stderrStat)
	if isFd12 {
		l.debug(sess.Log(), "daemon stdout and stderr will be rotated")
	}

	outputLastDir := filepath.Join(outputLogDir, OutputArchiveBatchDirName)
	prefix := strings.TrimSuffix(outputFileName, filepath.Ext(outputFileName)) + "_"
	outfile, err := rotatefile.Open(
		outputLogFilePath,
		rotatefile.RotatedFilePrefix(prefix),
		rotatefile.ArchiveDir(outputLastDir, 0),
	)
	if err != nil {
		return err
	}
	defer outfile.Close()

	if err := l.jobRotate(sess, outfile); err != nil {
		return fmt.Errorf("%w: failed to rotate file %s: %s", Error, outfile.Name(), err.Error())
	}

	if isFd12 {
		file, err := outfile.OpenFile(os.O_WRONLY | os.O_APPEND)
		if err != nil {
			return err
		}
		defer file.Close()

		fd := int(file.Fd())
		haderr := false
		if err := unix.Dup2(fd, 1); err != nil {
			haderr = true
			l.error(sess.Log(), fmt.Errorf("dup2 stdout: %w", err).Error())
		}
		if err := unix.Dup2(fd, 2); err != nil {
			haderr = true
			l.error(sess.Log(), fmt.Errorf("dup2 stderr: %w", err).Error())
		}
		if !haderr {
			l.debug(sess.Log(), fmt.Sprintf("redirected stdout and stderr to new file %s", outfile.Name()))
		}
	}

	if err := outfile.Close(); err != nil {
		l.error(sess.Log(), fmt.Errorf("close output file: %w", err).Error())
	}

	// Create batch
	batchDir := filepath.Join(
		sess.Settings().Get("daemon.fs.path.logs").String(),
		OutputArchiveBatchDirName,
	)

	cleanName := strings.TrimSuffix(outputFileName, filepath.Ext(outputFileName))

	if err := l.jobCreateBatch(sess, batchDir, cleanName); err != nil {
		return fmt.Errorf("%w: failed to create log output batch", err)
	}

	return nil
}

func (l *Logger) cronOutputArchiveSchedule(sess *session.Context) error {
	batchDir := filepath.Join(
		sess.Settings().Get("daemon.fs.path.logs").String(),
		OutputArchiveBatchDirName,
	)

	pruned, err := l.jobPruneEmptyFiles(sess, batchDir)
	if err != nil {
		l.warn(sess.Log(), "failed to prune empty output files", slog.String("err", err.Error()))
	} else if pruned > 0 {
		l.info(sess.Log(), "pruned empty log output files", slog.Int("count", pruned))
	}

	logdir := sess.Settings().Get("daemon.fs.path.logs").String()
	archiveDir := filepath.Join(logdir, "archive", OutputArchiveDirName)

	if _, err := os.Stat(batchDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	count, err := l.jobCreateArchive(sess, batchDir, archiveDir)
	if err != nil {
		return err
	}
	if count > 0 {
		l.statsUpdateLoggerFsStats(sess)
	}

	return l.jobCleanOldArchives(sess, archiveDir)
}

func (l *Logger) cronOutputExcessiveFileSize(sess *session.Context) error {
	if !l.ready.Load() {
		l.debug(sess.Log(), "skip log output size check, too early")
		return nil
	}

	rotationScheduleExpr := sess.Settings().Get("daemon.log.rotation_schedule").String()
	rotationSchedule, err := cron.ParseWithOptionalSecond(rotationScheduleExpr)
	if err != nil {
		return err
	}
	nextRotation := rotationSchedule.Next(time.Now())

	if nextRotation.IsZero() && time.Until(nextRotation) < time.Minute {
		l.debug(sess.Log(), "output rotation due to size job skipped to prevent concurrent rotation")
		return nil
	}

	maxsize, err := bytesize.Parse(sess.Settings().Get("daemon.log.max_file_size").Value().String())
	if err != nil {
		return fmt.Errorf("%w: failed to parse max log file size setting", Error)
	}

	outputFileName := sess.Settings().Get("daemon.log.output_file_name").String()
	if outputFileName == "" || outputFileName == "-" {
		return nil
	}

	outputLogDir := sess.Settings().Get("daemon.fs.path.logs").String()
	outputLogFilePath := filepath.Join(
		outputLogDir,
		outputFileName,
	)

	outputLastDir := filepath.Join(outputLogDir, OutputArchiveBatchDirName)
	prefix := strings.TrimSuffix(outputFileName, filepath.Ext(outputFileName)) + "_"
	outfile, err := rotatefile.Open(
		outputLogFilePath,
		rotatefile.RotatedFilePrefix(prefix),
		rotatefile.ArchiveDir(outputLastDir, 0),
	)
	if err != nil {
		return err
	}
	defer outfile.Close()

	stat, err := outfile.Stat()
	if err != nil {
		return fmt.Errorf("%w: failed to stat %s file", Error, outfile.Name())
	}
	// ok return
	if maxsize.Bytes() > stat.Size {
		return nil
	}

	l.notice(sess.Log(),
		fmt.Sprintf(
			"rotate due to size: %s > suggested size %s",
			bytesize.IECSize(stat.Size).String(),
			maxsize.String(),
		),
		slog.Uint64("current_size", stat.Size),
		slog.Uint64("max_size", maxsize.Bytes()),
		slog.String("file", outfile.Name()),
	)

	return l.cronOutputRotationSchedule(sess)
}

func (l *Logger) jobCleanOldArchives(sess *session.Context, archiveDir string) error {
	// cleanup
	archiveEntries, err := os.ReadDir(archiveDir)
	if err != nil {
		return err
	}
	if len(archiveEntries) == 0 {
		return nil
	}

	l.debug(sess.Log(),
		"clean up old output log archives",
		slog.String("archive_dir", archiveDir),
	)

	archiveRetentionPeriod := sess.Settings().Get("daemon.log.archive_retention_period").Value().Duration()
	for _, entry := range archiveEntries {
		archiveAbs := filepath.Join(archiveDir, entry.Name())

		stat, err := fsutils.Stat(archiveAbs)
		if err != nil {
			l.notice(sess.Log(), "failed to stat log archive", slog.String("err", err.Error()))
			continue
		}

		if entry.IsDir() {
			// for dirs use change time instead birth time
			shouldRemove := !stat.Ctime.IsZero() && stat.Ctime.Before(time.Now().Add(-archiveRetentionPeriod))
			if !shouldRemove {
				continue
			}
			if err := os.RemoveAll(archiveAbs); err != nil {
				l.warn(sess.Log(), "failed to delete old log archive", slog.String("err", err.Error()), slog.String("archive", filepath.Base(archiveAbs)))
				continue
			}

		} else {
			shouldRemove := !stat.Btime.IsZero() && stat.Btime.Before(time.Now().Add(-archiveRetentionPeriod))
			if !shouldRemove {
				continue
			}
			if err := os.Remove(archiveAbs); err != nil {
				l.warn(sess.Log(), "failed to delete old log archive", slog.String("err", err.Error()), slog.String("archive", filepath.Base(archiveAbs)))
				continue
			}
		}
		l.info(sess.Log(), "removed old log archive", slog.String("archive", filepath.Base(archiveAbs)))
	}

	return nil
}

func (l *Logger) jobExcessiveFileSize(sess *session.Context, file *rotatefile.File) error {
	rotationScheduleExpr := sess.Settings().Get("daemon.log.rotation_schedule").String()
	rotationSchedule, err := cron.ParseWithOptionalSecond(rotationScheduleExpr)
	if err != nil {
		return err
	}
	nextRotation := rotationSchedule.Next(time.Now())

	if nextRotation.IsZero() && time.Until(nextRotation) < time.Minute {
		l.debug(sess.Log(), "log rotation due to size job skipped to prevent concurrent rotation")
		return nil
	}

	defer l.statsUpdateLoggerFsStats(sess)

	maxsize, err := bytesize.Parse(sess.Settings().Get("daemon.log.max_file_size").Value().String())
	if err != nil {
		return fmt.Errorf("%w: failed to parse max log file size setting", Error)
	}

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: failed to stat %s file", Error, file.Name())
	}
	// ok return
	if maxsize.Bytes() > stat.Size {
		return nil
	}

	if err := file.Rotate(); err != nil {
		return err
	}

	l.notice(sess.Log(),
		fmt.Sprintf(
			"rotated due to size: %s > suggested size %s",
			bytesize.IECSize(stat.Size).String(),
			maxsize.String(),
		),
		slog.Uint64("current_size", stat.Size),
		slog.Uint64("max_size", maxsize.Bytes()),
		slog.String("file", file.Name()),
	)

	l.tel.UpdateLogger(func(ls *telemetry.Logger) {
		ls.Rotations++
	})

	return nil
}

func (l *Logger) jobRotate(sess *session.Context, file *rotatefile.File) error {
	if err := l.statsUpdateNextRotation(sess); err != nil {
		return err
	}

	defer l.statsUpdateLoggerFsStats(sess)

	rotationSchedule := sess.Settings().Get("daemon.log.rotation_schedule").String()
	l.debug(sess.Log(),
		fmt.Sprintf("scheduled rotation of %s", file.Name()),
		slog.String("scheduled", rotationSchedule),
		slog.String("file_name", file.Name()),
	)

	l.mu.Lock()
	if err := file.Rotate(); err != nil {
		l.mu.Unlock()
		return err
	}
	l.mu.Unlock()

	l.debug(sess.Log(),
		fmt.Sprintf("scheduled rotation of %s completed", file.Name()),
		slog.String("scheduled", rotationSchedule),
		slog.String("file_name", file.Name()),
	)
	l.tel.UpdateLogger(func(ls *telemetry.Logger) {
		ls.Rotations++
	})

	return nil
}

func (l *Logger) jobPruneEmptyFiles(sess *session.Context, dir string) (int, error) {
	if !sess.Settings().Get("daemon.log.keep_empty_logs").Value().Bool() {
		return 0, nil
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("%w: failed to list files to prune", Error)
	}

	var needsPruning []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			l.error(sess.Log(), err.Error())
			continue
		}
		if info.Size() == 0 {
			needsPruning = append(needsPruning, filepath.Join(dir, entry.Name()))
		}
	}

	if len(needsPruning) > 0 {
		l.debug(sess.Log(), fmt.Sprintf("pruning %d empty file(s)", len(needsPruning)))
		for _, name := range needsPruning {
			if err := os.Remove(name); err != nil {
				l.error(sess.Log(), err.Error())
			}
		}
	}

	return len(needsPruning), nil
}

func (l *Logger) jobCreateBatch(sess *session.Context, batchDir string, name string) error {
	if _, err := os.Stat(batchDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	oldest, newest, _, err := fsutils.DirBtimeSpan(batchDir, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// no files
			return nil
		}
		l.error(sess.Log(), err.Error())
		return fmt.Errorf("failed to stat log archive batch dir %s", batchDir)
	}

	files, err := os.ReadDir(batchDir)
	if err != nil {
		return fmt.Errorf("%w: failed to list files to batch: %s", Error, err.Error())
	}

	if len(files) == 0 {
		return nil
	}
	oldestStr := oldest.Format("20060102")
	newestStr := newest.Format("20060102")
	batchName := fmt.Sprintf("%s_%s", name, oldestStr)
	if oldestStr != newestStr {
		batchName = fmt.Sprintf("%s_%s_%s", name, oldestStr, newestStr)
	}

	currentBatchDir := getArchivePath(batchDir, batchName, "")

	if err := os.MkdirAll(currentBatchDir, 0750); err != nil {
		l.error(sess.Log(), err.Error())
		return fmt.Errorf("failed to create current file batch dir %s", batchDir)
	}
	count := 0
	for _, entry := range files {
		if entry.IsDir() {
			continue
		}
		srcpath := filepath.Join(batchDir, entry.Name())
		destpath := filepath.Join(currentBatchDir, entry.Name())

		if err := os.Rename(srcpath, destpath); err != nil {
			l.error(sess.Log(), err.Error())
			return fmt.Errorf("failed to move file %s to %s", srcpath, destpath)
		}
		count++
	}

	if count > 0 {
		l.debug(sess.Log(),
			fmt.Sprintf("moved %d log files to archive batch", count),
			slog.String("batch_name", batchName),
		)
	}

	return nil
}

func (l *Logger) jobCreateArchive(sess *session.Context, batchDir, archiveDir string) (int, error) {

	l.mu.Lock()
	defer l.mu.Unlock()

	files, err := os.ReadDir(batchDir)
	if err != nil {
		return 0, fmt.Errorf("%w: failed to list bathces: %s", Error, err.Error())
	}
	if len(files) == 0 {
		return 0, nil
	}

	if err := os.MkdirAll(archiveDir, 0750); err != nil {
		return 0, err
	}

	compDisabled := sess.Settings().Get("daemon.log.archive_compression_disabled").Value().Bool()

	count := 0
	barches := make(map[string]string)

	for _, entry := range files {
		if !entry.IsDir() {
			continue
		}
		srcpath := filepath.Join(batchDir, entry.Name())
		entryname, _, _ := strings.Cut(entry.Name(), ".") // remove local batch sequence
		var destpath string

		destpath = getArchivePath(archiveDir, entryname, "")

		if err := os.Rename(srcpath, destpath); err != nil {
			return 0, fmt.Errorf("%w: failed to archive logs from %s to %s: %s", err, srcpath, destpath, err.Error())
		}
		count++
		barches[filepath.Base(destpath)] = destpath
	}

	// no compression
	if compDisabled || count == 0 {
		return count, nil
	}

	l.debug(sess.Log(), fmt.Sprintf("archive %d batches", count))

	// compress
	for name, dirpath := range barches {
		oldest, newest, _, err := fsutils.DirBtimeSpan(dirpath, false)
		if err != nil {
			l.error(sess.Log(), err.Error())
		}

		entryname, _, _ := strings.Cut(strings.TrimSuffix(name, ".tar.gz"), ".")

		tarpath := getArchivePath(archiveDir, entryname, ".tar.gz")

		if err := fsutils.BackupDir(context.Background(), dirpath, tarpath, true, nil); err != nil {
			return count, fmt.Errorf("failed to compress log archive batch :%s", err.Error())
		}

		l.info(sess.Log(), fmt.Sprintf("archived %s", name),
			slog.String("archive", filepath.Base(tarpath)),
			slog.Time("from", oldest),
			slog.Time("to", newest),
		)
	}
	if count > 0 {
		l.debug(sess.Log(), fmt.Sprintf("compressed %d batches", count))
	}

	return count, nil
}
