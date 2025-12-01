// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package loggingd provides configuration for the daemon's logging service.
package logd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/sdk/session"
)

const (
	ServiceSlug               = "daemon-logd"
	OutputArchiveBatchDirName = "last_outputs"
	OutputArchiveDirName      = "output_archives"
	LogArchiveBatchDirName    = "last_logs"
	LogArchiveDirName         = "log_archives"
	BackupPrefix              = "logs_backup_"
)

var (
	Error = fmt.Errorf(ServiceSlug)
)

// Settings defines the configuration for the daemon's logging service, managing
// the log file's directory and name. It is used to customize logging.
type Settings struct {
	// ArchiveAfter specifies the age threshold after which log files are archived/compressed.
	// Files older than this duration are moved to an archive directory during rotation or cleanup.
	// Set it to '-' to disable archiving.
	ArchiveSchedule cron.Expression `default:"@weekly" mutation:"once"`

	// ArchiveCompressionDisabled specifies whether to compress archived log files.
	ArchiveCompressionDisabled settings.Bool `default:"false" mutation:"once"`

	// ArchiveRetentionPeriod specifies the maximum age of log files and compressed archives
	// before automatic deletion. Both uncompressed log files and compressed archives (e.g., gzip)
	// older than this duration are deleted during rotation or cleanup.
	// Default is 180 days.
	ArchiveRetentionPeriod settings.Duration `default:"4320h" mutation:"once"`

	// DirName specifies the directory name where the daemon's log file is written.
	// final log dir will be {app.fs.path.profile.logs}/{LogDirName}
	// It is immutable after initial configuration and defaults to
	DirName settings.String `mutation:"once" default:"daemon"`

	// Disabled determines whether the daemon's logging service is enabled.
	// When true, the daemon's logging service is entirely disabled, including
	// stdout/stderr redirection and file writing, allowing the application's custom
	// Logger interface (with built-in or custom adapters) to handle all logging as
	// configured at the application level without interference.
	// When false (default), the daemon controls logging, writing to the specified log
	// file (see DirName and FileName) exclusively in background mode or to both the
	// log file and stdout/stderr in foreground mode. This setting is immutable after
	// initial configuration.
	Disabled settings.Bool `default:"false"`

	// LogFileName specifies the name of the daemon's latest log file.
	// Defaults to "daemon.log".
	LogFileName settings.String `default:"daemon.log"`

	// OutputFileName specifies the name of the daemon's latest log file for stdout/stderr.
	// Defaults to "output.log".
	// Setting it to "-" disables logging stdout/stderr.
	// and daemon process stdrout/stderr are directed to
	// io.Discard
	OutputFileName settings.String `default:"output.log"`

	// KeepEmptyLogs prevents deletion of empty log files during rotation.
	// When false (default), empty log files are pruned on rotation.
	// When true, empty log files are retained, and a new {FileName} is created.
	KeepEmptyLogs settings.Bool `default:"false" mutation:"once"`

	// MaxFileSize specifies the maximum size of a log file before rotation.
	// It uses IECSize for base-2 units (e.g., 1 MiB = 2^20 bytes).
	// Defaults to "100 MiB". 0 value uses default.
	MaxFileSize bytesize.IECSize `default:"100 MiB" mutation:"once"`

	// StartupRotationDisabled controls log file rotation on daemon startup, even if the rotation
	// deadline (e.g., daily) is not reached. When false (default), the current log file
	// (e.g., `daemon.log`) is renamed with a timestamp (e.g., `daemon-2025-08-20.log`) or a
	// numbered suffix (e.g., `daemon-2025-08-20.log.1`) if the name exists. A new
	// `daemon.log` is created for the current run. If false, rotation follows the
	// scheduled deadline. This setting is immutable.
	StartupRotationDisabled settings.Bool `default:"false" mutation:"once"`

	// RotationSchedule specifies the interval at which log files are rotated.
	// It uses cron expressions to define the rotation schedule.
	// Default is "@daily" (rotate daily at midnight).
	// '-' disables log rotation cron, logs are still rotated
	// on daemon startup if StartupRotationDisabled is false or
	// on excessive size when MaxFileSize is greater than 0..
	RotationSchedule cron.Expression `default:"@daily" mutation:"once"`
}

func (s *Settings) Blueprint() (*settings.Blueprint, error) {
	bp, err := settings.New(s)
	if err != nil {
		return nil, err
	}

	return bp, nil
}

func (s *Settings) Defaults() {}

func Options() []*options.OptionSpec {
	return []*options.OptionSpec{
		options.NewOption("log.file", ""),
		options.NewOption("log.output", ""),
	}
}

func DaemonLog(logger *logging.Logger, lvl logging.Level, label string, pid int64, msg string, args ...slog.Attr) {
	args = append(args, slog.Group("daemon",
		slog.String("label", label),
		slog.Int64("pid", pid),
	))
	logger.LogDepth(2, lvl, msg, args...)
}

func GetNextBackupPaths(sess *session.Context) (dir, archive string, err error) {
	backupsDir := filepath.Join(sess.Get("daemon.fs.path.backups").String(), "logs")
	if err = os.MkdirAll(backupsDir, 0755); err != nil {
		return dir, archive, err
	}
	archive = getArchivePath(backupsDir, fmt.Sprintf("%s%s", BackupPrefix, time.Now().Format("20060102")), ".tar.gz")
	dir = strings.TrimSuffix(archive, ".tar.gz")
	return dir, archive, nil
}

// getArchivePath constructs correct name for next available sequence
// Uses zero-padded 5-digit sequence numbers for proper lexicographic sorting
// e.g. tarpath := getArchivePath(archiveDir, archiveName, ".tar.gz")
func getArchivePath(archiveDir, archiveName, ext string) string {
	archivePath := filepath.Join(archiveDir, archiveName+ext)

	// If base archive doesn't exist, return it
	if _, err := os.Stat(archivePath); err != nil {
		return archivePath
	}

	// Use shared helper to find max sequence (note: this is a standalone function,
	// so we'll inline the logic here or make findMaxSequenceInDir a standalone function)
	maxSequence := findMaxSequenceInDir(archiveDir, archiveName, ext)

	// Return next sequence with zero-padding (5 digits)
	nextSequence := maxSequence + 1
	return filepath.Join(archiveDir, fmt.Sprintf("%s.%05d%s", archiveName, nextSequence, ext))
}

// findMaxSequenceInDir is a shared helper that finds the highest sequence number
// in a directory for files matching the pattern nameWithoutExt.XXXXX.ext
// Made standalone so it can be used by both methods and standalone functions
func findMaxSequenceInDir(dir, nameWithoutExt, ext string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	maxSequence := 0
	expectedPrefix := nameWithoutExt + "."

	for _, entry := range entries {
		name := entry.Name()

		// Must match our pattern: nameWithoutExt.XXXXX.ext
		if !strings.HasPrefix(name, expectedPrefix) || !strings.HasSuffix(name, ext) {
			continue
		}

		// Extract sequence part
		middle := strings.TrimPrefix(strings.TrimSuffix(name, ext), expectedPrefix)

		// Sequence should be only digits
		if !isAllDigits(middle) {
			continue
		}

		// Parse and track max sequence
		if seq, err := strconv.Atoi(middle); err == nil && seq > 0 && seq > maxSequence {
			maxSequence = seq
		}
	}

	return maxSequence
}

// isAllDigits checks if a string contains only digits
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
