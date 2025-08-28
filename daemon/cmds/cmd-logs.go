// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package cmds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/happy-sdk/addons/daemon/services/logd"
	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/logging/adapters/console"
	"github.com/happy-sdk/happy/pkg/scheduling/cron"
	"github.com/happy-sdk/happy/pkg/settings"
	"github.com/happy-sdk/happy/pkg/strings/humanize"
	"github.com/happy-sdk/happy/pkg/strings/textfmt"
	"github.com/happy-sdk/happy/pkg/tui/ansicolor"
	"github.com/happy-sdk/happy/sdk/action"
	"github.com/happy-sdk/happy/sdk/cli"
	"github.com/happy-sdk/happy/sdk/cli/command"
	"github.com/happy-sdk/happy/sdk/session"
	"github.com/happy-sdk/lib/tail"
)

func Logs(category string) *command.Command {
	cmd := command.New("logs", command.Config{
		Description: "Manage and display daemon logs",
		Category:    settings.String(category),
	})

	cmd.WithSubCommands(logsBackup())
	cmd.WithSubCommands(logsStat())
	cmd.WithSubCommands(logsTail())
	return cmd
}

func logsBackup() *command.Command {
	cmd := command.New("backup", command.Config{
		Description: "Backup creates single archive from all ",
	})

	cmd.Do(func(sess *session.Context, args action.Args) error {
		return nil
	})

	return cmd
}

func logsStat() *command.Command {
	cmd := command.New("stat", command.Config{
		Description: "Display stats of latest log file",
	})

	cmd.WithFlags(
		cli.NewBoolFlag("ago", false, "display timestamps in ago format", "a"),
		cli.NewBoolFlag("output", false, "display info only about daemon output logs", "o"),
		cli.NewBoolFlag("logs", false, "display info about daemon logs", "l"),
	)

	cmd.Do(func(sess *session.Context, args action.Args) error {

		logdir := sess.Settings().Get("daemon.fs.path.logs").String()

		// Log dir
		dirsize, err := fsutils.DirSize(logdir)
		if err != nil {
			return err
		}
		tadirsize, _ := fsutils.DirSize(filepath.Join(logdir, "archive"))

		filec, _, err := fsutils.CountFilesAndDirs(logdir)
		if err != nil {
			return err
		}

		info := textfmt.NewTable(
			textfmt.TableTitle(fmt.Sprintf("Dir: %s", logdir)),
		)

		info.AddRow("Total size:", bytesize.SISize(dirsize).String())
		info.AddRow("Total archive size:", bytesize.SISize(tadirsize).String())
		info.AddRow("Total files:", fmt.Sprint(filec))
		info.AddRow("Archive schedule:", sess.Settings().Get("daemon.log.archive_schedule").String())
		info.AddRow("Retention period:", sess.Settings().Get("daemon.log.archive_retention_period").Value().Duration().String())

		op := args.Flag("output").Present()
		lp := args.Flag("logs").Present()

		if ltbl, err := logStatFileInfoTable(sess, args, logdir, false); err != nil {
			fmt.Println(info.String())
			return err
		} else if lp || (!lp && !op) {
			info.Append(ltbl)
		}

		if otbl, err := logStatFileInfoTable(sess, args, logdir, true); err != nil {
			fmt.Println(info.String())
			return err
		} else if op || (!lp && !op) {
			info.Append(otbl)
		}
		fmt.Println(info.String())
		return nil
	})

	return cmd
}
func logStatFileInfoTable(sess *session.Context, args action.Args, logdir string, output bool) (*textfmt.Table, error) {
	var (
		title          = "LOG FILES"
		fileArchiveDir = filepath.Join(logdir, "archive", logd.LogArchiveDirName)
		batchDir       = filepath.Join(logdir, logd.LogArchiveBatchDirName)
		logFileName    = sess.Settings().Get("daemon.log.log_file_name").String()
		filePath       = sess.Opts().Get("daemon.log.file").String()
	)
	if output {
		title = "OUTPUT FILES"
		fileArchiveDir = filepath.Join(logdir, "archive", logd.OutputArchiveDirName)
		batchDir = filepath.Join(logdir, logd.OutputArchiveBatchDirName)
		logFileName = sess.Settings().Get("daemon.log.output_file_name").String()
		filePath = filepath.Join(logdir, logFileName)
	}

	// Archive dir may not exist
	adirsize, _ := fsutils.DirSize(fileArchiveDir)
	bdirsize, _ := fsutils.DirSize(batchDir)

	finfo := textfmt.NewTable(
		textfmt.TableTitle(title),
	)
	finfo.AddRow("Archives:", fileArchiveDir)
	finfo.AddRow("Batch dir:", batchDir)
	finfo.AddRow("Current batch size:", bytesize.SISize(bdirsize).String())
	finfo.AddRow("Archive size:", bytesize.SISize(adirsize).String())

	if _, err := os.Stat(filePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return finfo, nil
		} else {
			return nil, err
		}
	}

	cleanName := strings.TrimSuffix(logFileName, filepath.Ext(logFileName)) + "_"

	// Latest logfile
	file, err := rotatefile.Open(
		filePath,
		rotatefile.RotatedFilePrefix(cleanName),
		rotatefile.ArchiveDir(batchDir, rotatefile.DefaultArchivePerm),
	)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	tss, err := file.Stat()
	if err != nil {
		return nil, err
	}

	latest := textfmt.NewTable(
		textfmt.TableTitle(fmt.Sprintf("Latest: %s", filePath)),
	)

	latest.AddRow(
		fmt.Sprintf("Size: %s", bytesize.SISize(tss.Size).String()),
		fmt.Sprintf("Blocks: %d", tss.Blocks),
		fmt.Sprintf("IO Block: %d", tss.Blksize),
		fmt.Sprintf("Inode: %d", tss.Ino),
		fmt.Sprintf("Links: %d", tss.Nlink),
	)

	acct := textfmt.NewTable()
	acct.AddRow(
		"Access:",
		fmt.Sprintf("(%04o/%s)", tss.Mode&0777, os.FileMode(tss.Mode).String()),
		fmt.Sprintf("User: %d", tss.Uid),
		fmt.Sprintf("Group: %d", tss.Gid),
	)

	latest.Append(acct)

	sectx, _ := file.SELinuxContext()
	fstat := textfmt.NewTable()
	fstat.AddRow("Context:", sectx)
	fstat.AddDivider()

	lastRotation := file.PreviousRotation()
	if lastRotation.IsZero() {
		fstat.AddRow("Previous rotation:", "unknown")
	} else {
		lastRotationStr := lastRotation.Format(time.RFC3339)
		if args.Flag("ago").Present() {
			lastRotationStr = humanize.Duration(time.Since(lastRotation), true) + " ago"
		}

		fstat.AddRow("Rotations:", fmt.Sprint(file.Rotations()))
		fstat.AddRow("Previous rotation:", lastRotationStr)
	}

	rotationScheduleExpr := sess.Settings().Get("daemon.log.rotation_schedule").String()
	rotationSchedule, err := cron.ParseWithOptionalSecond(rotationScheduleExpr)
	if err != nil {
		return nil, err
	}
	nextRotation := rotationSchedule.Next(time.Now())
	nextRotationStr := nextRotation.Format(time.RFC3339)
	if args.Flag("ago").Present() {
		nextRotationStr = "in " + humanize.Duration(time.Until(nextRotation), true)
	}
	fstat.AddRow("Next rotation:", nextRotationStr)

	fstat.AddDivider()
	atimeStr := tss.Atime.Format(time.RFC3339)
	mtimeStr := tss.Mtime.Format(time.RFC3339)
	ctimeStr := tss.Ctime.Format(time.RFC3339)
	btimeStr := tss.Btime.Format(time.RFC3339)

	if args.Flag("ago").Present() {
		atimeStr = humanize.Duration(time.Since(tss.Atime), true) + " ago"
		mtimeStr = humanize.Duration(time.Since(tss.Mtime), true) + " ago"
		ctimeStr = humanize.Duration(time.Since(tss.Ctime), true) + " ago"
		btimeStr = humanize.Duration(time.Since(tss.Btime), true) + " ago"
	}

	fstat.AddRow("Access:", atimeStr)
	fstat.AddRow("Modify:", mtimeStr)
	fstat.AddRow("Change:", ctimeStr)
	fstat.AddRow("Birth:", btimeStr)

	latest.Append(fstat)
	finfo.Append(latest)
	return finfo, nil
}

func logsTail() *command.Command {
	cmd := command.New("tail", command.Config{
		Description: "Display the last lines of the log file",
	})

	cmd.WithFlags(
		cli.NewStringFlag("filter-keys", "", "Output only log lines with structured log attributes containing any of the comma-separated keys."),
		cli.NewStringFlag("filter-kv", "", "Output only log lines with a structured log attribute matching the key=value pair (e.g., 'key=value')."),
		cli.NewBoolFlag("follow", false, "Stream appended log data as the file grows.", "f"),
		cli.NewStringFlag("level", "", "Filter logs by minimum log level (e.g., 'info', 'debug')."),
		cli.NewIntFlag("lines", 10, "Output the last NUM lines (default: 10). Use +NUM to skip NUM-1 lines from the start.", "n"),
		cli.NewBoolFlag("output", false, "Tail output daemon stdout/stderr instead of log file", "o"),
		cli.NewBoolFlag("single", false, "With -f, exit after the daemon process terminates (single run mode).", "s"),
	)

	cmd.Do(func(sess *session.Context, args action.Args) error {
		logfilePath := sess.Get("daemon.log.file").String()
		outfilePath := sess.Get("daemon.log.output").String()

		tailFilePath := logfilePath

		if args.Flag("output").Present() {
			tailFilePath = outfilePath
		}

		if _, err := os.Stat(tailFilePath); err != nil {
			return err
		}

		// Allow user to cancel with Ctrl-C
		sess.Wait(false)

		opts, err := sess.Log().Options()
		if err != nil {
			return err
		}
		opts.LevelVar = nil
		opts.SetSlogOutput = false
		opts.Level = math.MinInt
		if args.Flag("level").Present() {
			opts.Level, _ = logging.LevelFromString(args.Flag("level").String())
		}
		opts.AddSource = false

		logger := console.NewHandler(
			sess.Context(),
			os.Stdout,
			opts,
			ansicolor.New(),
		)

		// Tail and follow the log file
		if args.Flag("follow").Var().Bool() {
			ctx, cancel := context.WithCancel(sess.Context())
			defer cancel()

			// Check if we need to monitor the process and exit when daemon exits
			if args.Flag("single").Var().Bool() &&
				sess.Get("daemon.process.running").Bool() {

				pid := sess.Get("daemon.process.pid").Int()
				go func() {
					err := monitorProcess(ctx, pid, func() {
						cancel()
					})
					if err != nil {
						sess.Log().Error(fmt.Sprintf("PROCESS MONITOR: %s", err.Error()))
					}
				}()
			}

			for line, err := range tail.TailFile(ctx, tailFilePath, args.Flag("lines").Var().Int()) {
				if err != nil {
					sess.Log().Error(fmt.Sprintf("TAIL FILE: %s", err.Error()))
					break
				}
				logPrintln(ctx, logger, opts.TimestampFormat, line)
			}
			return nil
		}

		// No follow
		var (
			seen int
			end  = args.Flag("lines").Var().Int()
		)
		ctx, cancel := context.WithTimeoutCause(sess.Context(), time.Second*2, errors.New("no log entries in 2 seconds"))
		defer cancel()

		// Tail the log file
		for line, err := range tail.TailFile(ctx, tailFilePath, args.Flag("lines").Var().Int()) {
			if err != nil {
				sess.Log().Error(fmt.Sprintf("TAIL FILE: %s", err.Error()))
				break
			}
			seen++
			logPrintln(ctx, logger, opts.TimestampFormat, line)
			if seen == end {
				cancel()
				break
			}
		}

		return nil
	})

	return cmd
}

type entry map[string]any

func logPrintln(ctx context.Context, logger *console.Handler, tsFormat string, line string) {
	if !strings.HasPrefix(line, "{") {
		fmt.Println(line)
		return
	}
	var (
		e entry
		r slog.Record
	)
	if err := json.Unmarshal([]byte(line), &e); err != nil {
		r.Level = slog.LevelError
		r.Message = "failed to parse log line"
		r.Time = time.Now()
		fmt.Println("LINE:", line)
	} else {
		// level
		if lvlStr, ok := e["level"].(string); ok {
			lvl, err := logging.LevelFromString(lvlStr)
			if err != nil {
				r.Level = slog.LevelError
				r.Message = fmt.Sprintf("failed to parse log level: %s", err.Error())
				r.Time = time.Now()
			} else {
				r.Level = slog.Level(lvl)
			}
			delete(e, "level")
		} else {
			r.Level = slog.LevelError
		}
		if !logger.Enabled(ctx, r.Level) {
			return
		}

		// timestamp
		if tsStr, ok := e["time"].(string); ok {
			ts, err := time.Parse(tsFormat, tsStr)
			if err != nil {
				r.Level = slog.LevelError
				r.Message = fmt.Sprintf("failed to parse timestamp: %s", err.Error())
				r.Time = time.Now()
			} else {
				r.Time = ts
			}
			delete(e, "time")
		} else {
			r.Time = time.Now()
		}

		// message
		if msg, ok := e["msg"].(string); ok {
			r.Message = msg
			delete(e, "msg")
		}
	}
	for k, v := range e {
		r.Add(k, v)
	}

	logger.Handle(ctx, r)
}
