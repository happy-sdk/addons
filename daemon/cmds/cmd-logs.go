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
	"time"

	"github.com/happy-sdk/happy/pkg/bytesize"
	"github.com/happy-sdk/happy/pkg/fsutils"
	"github.com/happy-sdk/happy/pkg/fsutils/rotatefile"
	"github.com/happy-sdk/happy/pkg/logging"
	"github.com/happy-sdk/happy/pkg/logging/adapters/console"
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

	cmd.WithSubCommands(logsStat())
	cmd.WithSubCommands(logsTail())
	return cmd
}

func logsStat() *command.Command {
	cmd := command.New("stat", command.Config{
		Description: "Display stats of latest log file",
	})

	cmd.WithFlags(
		cli.NewBoolFlag("ago", false, "display timestamps in ago format"),
	)
	cmd.Do(func(sess *session.Context, args action.Args) error {
		logfilePath := sess.Get("daemon.log.file").String()
		logdirPath := sess.Get("daemon.log.dir").String()

		// Log dir
		dirsize, err := fsutils.DirSize(logdirPath)
		if err != nil {
			return err
		}

		adirsize, err := fsutils.DirSize(filepath.Join(logdirPath, "archive"))
		if err != nil {
			return err
		}

		filec, _, err := fsutils.CountFilesAndDirs(logdirPath)
		if err != nil {
			return err
		}

		info := textfmt.NewTable(
			textfmt.TableTitle(fmt.Sprintf("Dir: %s", logdirPath)),
		)

		info.AddRow("Total size:", bytesize.SISize(dirsize).String())
		info.AddRow("Total files:", fmt.Sprint(filec))

		ainfo := textfmt.NewTable(
			textfmt.TableTitle(fmt.Sprintf("Archive: %s", filepath.Join(logdirPath, "archive"))),
		)
		ainfo.AddRow("Batch dir:", filepath.Join(logdirPath, "last"))
		ainfo.AddRow("Archive size:", bytesize.SISize(adirsize).String())
		ainfo.AddRow("Archive after:", sess.Settings().Get("daemon.log.archive_after").Value().Duration().String())
		ainfo.AddRow("Batch period:", sess.Settings().Get("daemon.log.archive_batch_period").Value().Duration().String())
		ainfo.AddRow("Retention period:", sess.Settings().Get("daemon.log.archive_retention_period").Value().Duration().String())

		info.Append(ainfo)

		if _, err := os.Stat(logfilePath); err != nil {
			fmt.Println(info.String())
			if errors.Is(err, os.ErrNotExist) {
				return nil
			} else {
				return err
			}
		}

		// Latest logfile
		file, err := rotatefile.Open(
			logfilePath,
			rotatefile.RotatedFilePrefix("daemon_"),
		)
		if err != nil {
			return err
		}
		defer file.Close()

		tss, err := file.Stat()
		if err != nil {
			return err
		}

		filet := textfmt.NewTable(
			textfmt.TableTitle(fmt.Sprintf("Latest: %s", logfilePath)),
		)

		filet.AddRow(
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

		filet.Append(acct)

		sectx, _ := file.SELinuxContext()
		fstat := textfmt.NewTable()
		fstat.AddRow("Context:", sectx)
		fstat.AddDivider()

		lastRotation := file.PreviousRotation()
		if lastRotation.IsZero() {
			fstat.AddRow("Previous rotation:", "never")
		} else {
			lastRotationStr := lastRotation.Format(time.RFC3339)
			if args.Flag("ago").Present() {
				lastRotationStr = humanize.Duration(time.Since(lastRotation), true) + " ago"
			}

			fstat.AddRow("Rotations:", fmt.Sprint(file.Rotations()))
			fstat.AddRow("Previous rotation:", lastRotationStr)
		}

		rotationStateFile := filepath.Join(sess.Opts().Get("daemon.log.dir").String(), "next.rotation")
		if nextb, err := os.ReadFile(rotationStateFile); err != nil {
			fstat.AddRow("Next rotation:", "unknown")
		} else {
			nextRotation, err := time.Parse(time.RFC3339, string(nextb))
			nextRotationStr := "unknown"
			if err != nil {
				sess.Log().Error(fmt.Sprintf("failed to parse next rotation time: %s", err.Error()))
			} else {
				nextRotationStr = string(nextb)
				if args.Flag("ago").Present() {
					nextRotationStr = "in " + humanize.Duration(time.Until(nextRotation), true)
				}
			}
			fstat.AddRow("Next rotation:", nextRotationStr)
		}

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

		filet.Append(fstat)
		info.Append(filet)

		fmt.Println(info.String())

		return nil
	})

	return cmd
}
func logsTail() *command.Command {
	cmd := command.New("tail", command.Config{
		Description: "Display the last lines of the log file",
	})

	cmd.WithFlags(
		cli.NewIntFlag("lines", 10, "Output the last NUM lines (default: 10). Use +NUM to skip NUM-1 lines from the start.", "n"),
		cli.NewBoolFlag("follow", false, "Stream appended log data as the file grows.", "f"),
		cli.NewBoolFlag("single", false, "With -f, exit after the daemon process terminates (single run mode)."),
		cli.NewStringFlag("level", "", "Filter logs by minimum log level (e.g., 'info', 'debug')."),
		cli.NewStringFlag("filter-keys", "", "Output only log lines with structured log attributes containing any of the comma-separated keys."),
		cli.NewStringFlag("filter-kv", "", "Output only log lines with a structured log attribute matching the key=value pair (e.g., 'key=value')."),
	)

	cmd.Do(func(sess *session.Context, args action.Args) error {
		logfilePath := sess.Get("daemon.log.file").String()
		if _, err := os.Stat(logfilePath); err != nil {
			return err
		}

		// Allow user to cancel with Ctrl-C
		sess.Wait(false)

		ctx, cancel := context.WithCancel(sess)
		defer cancel()

		// Check if we need to monitor the process and exit when daemon exits
		if args.Flag("follow").Var().Bool() &&
			args.Flag("single").Var().Bool() &&
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

		// Tail the log file
		lines, errs, err := tail.TailFile(ctx, logfilePath, args.Flag("lines").Var().Int())
		if err != nil {
			return err
		}

		go func() {
			for err := range errs {
				sess.Log().Warn(fmt.Sprintf("TAIL: %s", err.Error()))
			}
		}()
		opts, err := sess.Log().Options()
		if err != nil {
			return err
		}
		opts.SetSlogOutput = false
		opts.Level = math.MinInt
		opts.AddSource = false

		logger := console.NewHandler(
			sess.Context(),
			os.Stdout,
			opts,
			ansicolor.New(),
		)

		// Tail and follow the log file
		if args.Flag("follow").Var().Bool() {
			for line := range lines {
				logPrintln(logger, opts.TimestampFormat, line)
			}
			return nil
		}

		// No follow
		if !args.Flag("follow").Var().Bool() {
			var (
				seen    int
				end     = args.Flag("lines").Var().Int()
				started = time.Now()
			)
			for line := range lines {
				seen++
				logPrintln(logger, opts.TimestampFormat, line)
				if seen == end {
					break
				}
				if time.Since(started) > time.Second*2 {
					break
				}
			}

			return nil
		}

		return nil
	})

	return cmd
}

type entry map[string]any

func logPrintln(logger *console.Handler, tsFormat string, line string) {
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
	logger.Handle(context.Background(), r)
}
