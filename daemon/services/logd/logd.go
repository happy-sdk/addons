// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

// Package loggingd provides configuration for the daemon's logging service.
package logd

import (
	"fmt"
	"log/slog"

	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/options"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/pkg/settings"
)

const ServiceName = "daemon-logd"

var (
	Error = fmt.Errorf(ServiceName)
)

// Settings defines the configuration for the daemon's logging service, managing
// the log file's directory and name. It is used to customize logging.
type Settings struct {
	// ArchiveAfter specifies the age threshold after which log files are archived.
	// Files older than this duration are moved to an archive directory during rotation or cleanup.
	// Default is older than 7 days. -1 to disable archiving.
	ArchiveAfter settings.Duration `default:"168h" mutation:"once"`

	// ArchiveBatchPeriod specifies the timespan for batching logs archives.
	// Logs within this period are grouped into a single archive (e.g., daily or weekly batches).
	// Default is 24h (daily batches).
	ArchiveBatchPeriod settings.Duration `default:"24h" mutation:"once"`

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

	// FileName specifies the name of the daemon's latest log file.
	// Defaults to "latest.log".
	FileName settings.String `default:"latest.log"`

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

	// RotationInterval specifies the interval at which log files are rotated.
	// It uses cron expressions to define the rotation schedule.
	// Default is "@daily" (rotate daily at midnight).
	// '-' disables log rotation cron, logs are still rotated
	// on daemon startup if StartupRotationDisabled is false or
	// on excessive size when MaxFileSize is greater than 0..
	RotationInterval cron.Expression `default:"@daily" mutation:"once"`
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
		options.NewOption("log.dir", ""),
	}
}

func DaemonLog(logger logging.Logger, lvl logging.Level, label string, pid int64, msg string, args ...slog.Attr) {
	args = append(args, slog.Group("daemon",
		slog.String("label", label),
		slog.Int64("pid", pid),
	))
	logger.LogDepth(2, lvl, msg, args...)
}
