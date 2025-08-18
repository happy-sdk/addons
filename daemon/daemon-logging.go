// SPDX-License-Identifier: Apache-2.0
//
// Copyright © 2025 The Happy Authors

package daemon

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/happy-sdk/happy/pkg/logging"
)

func log(logger logging.Logger, lvl logging.Level, svc string, pid int64, msg string, args ...slog.Attr) {
	logger.LogDepth(2, lvl, fmt.Sprintf("%s(%d): %s", svc, pid, msg), args...)
}

type logFile struct {
	mu   sync.Mutex
	name string
	file *os.File
}

func openLogFile(name string) (*logFile, error) {
	rw := &logFile{
		name: name,
	}
	if _, err := os.Stat(name); err == nil {
		rw.file, err = os.Open(name)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file: %w", err)
		}
	}
	return rw, nil
}

func (l *logFile) Write(b []byte) (n int, err error) {
	l.mu.Lock()
	n, err = l.file.Write(b)
	l.mu.Unlock()
	return
}

func (l *logFile) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// Rotate rotates the log file using best practices:
// - Current log: daemon.log
// - Daily rotation: daemon-2006-01-02.log
// - Multiple restarts same day: daemon-2006-01-02.log.1, daemon-2006-01-02.log.2, etc.
func (l *logFile) Rotate() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rotate()
}

func (l *logFile) rotate() error {
	// Check if current log file exists
	if _, err := os.Stat(l.name); errors.Is(err, os.ErrNotExist) {
		return l.createNewLogFile()
	}

	creationTime, err := l.readCreationTime()
	if err != nil {
		return fmt.Errorf("failed to read creation time: %w", err)
	}

	// Generate rotated filename based on file's last modification time
	rotatedName := l.generateRotatedName(creationTime)

	// If rotated file already exists, find next sequence number
	if _, err := os.Stat(rotatedName); err == nil {
		rotatedName = l.findNextSequenceName(rotatedName)
	}

	// Rename current log to rotated name
	if err := os.Rename(l.name, rotatedName); err != nil {
		return fmt.Errorf("failed to rotate log file: %w", err)
	}
	return l.createNewLogFile()
}

// generateRotatedName creates the rotated filename based on the file's timestamp
func (l *logFile) generateRotatedName(timestamp time.Time) string {
	dir := filepath.Dir(l.name)
	base := filepath.Base(l.name)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	dateStr := timestamp.Format("2006-01-02")
	return filepath.Join(dir, fmt.Sprintf("%s-%s%s", nameWithoutExt, dateStr, ext))
}

// findNextSequenceName finds the next available sequence number for same-day rotations
func (l *logFile) findNextSequenceName(baseName string) string {
	dir := filepath.Dir(baseName)
	basePattern := filepath.Base(baseName)

	// Find existing files with sequence numbers
	entries, err := os.ReadDir(dir)
	if err != nil {
		// If we can't read directory, just use .1
		return baseName + ".1"
	}

	maxSequence := 0
	for _, entry := range entries {
		name := entry.Name()

		// Check if file matches our pattern (e.g., daemon-2006-01-02.log.N)
		if strings.HasPrefix(name, basePattern+".") {
			suffix := strings.TrimPrefix(name, basePattern+".")
			if seq, err := strconv.Atoi(suffix); err == nil && seq > maxSequence {
				maxSequence = seq
			}
		}
	}

	return fmt.Sprintf("%s.%d", baseName, maxSequence+1)
}

// createNewLogFile creates a new log file and updates the file handle
func (l *logFile) createNewLogFile() error {
	// Create new file
	newFile, err := os.OpenFile(l.name, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		return fmt.Errorf("failed to create new log file: %w", err)
	}

	// Close old file if it exists
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			_ = newFile.Close()
			return fmt.Errorf("failed to close old log file: %w", err)
		}
	}

	_, _ = newFile.Write(logHeader())
	// Update file handle
	l.file = newFile
	return nil
}

// RotateIfNeeded rotates the log if it meets rotation criteria
func (l *logFile) RotateIfNeeded() (rotated bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := os.Stat(l.name); errors.Is(err, os.ErrNotExist) {
		return false, nil // No file to rotate
	}

	// Read the creation timestamp from the first line
	creationTime, err := l.readCreationTime()
	if err != nil {
		return false, fmt.Errorf("failed to read creation time: %w", err)
	}

	// Check if rotation is needed
	now := time.Now()
	creationDate := creationTime.Format("2006-01-02")
	currentDate := now.Format("2006-01-02")

	if creationDate != currentDate {
		return true, l.rotate()
	}

	return false, nil
}

// CleanupOldLogs removes log files older than the specified number of days
func (l *logFile) CleanupOldLogs(keepDays int) error {
	if keepDays <= 0 {
		return nil
	}

	dir := filepath.Dir(l.name)
	base := filepath.Base(l.name)
	ext := filepath.Ext(base)
	nameWithoutExt := strings.TrimSuffix(base, ext)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read log directory: %w", err)
	}

	cutoffDate := time.Now().AddDate(0, 0, -keepDays)
	var filesToDelete []string

	for _, entry := range entries {
		name := entry.Name()

		// Skip current log file
		if name == base {
			continue
		}

		// Check if it's a rotated log file (pattern: name-YYYY-MM-DD.ext or name-YYYY-MM-DD.ext.N)
		if strings.HasPrefix(name, nameWithoutExt+"-") {
			dateStr := extractDateFromLogName(name, nameWithoutExt, ext)
			if dateStr != "" {
				if logDate, err := time.Parse("2006-01-02", dateStr); err == nil {
					if logDate.Before(cutoffDate) {
						filesToDelete = append(filesToDelete, filepath.Join(dir, name))
					}
				}
			}
		}
	}

	// Sort files to delete oldest first
	sort.Strings(filesToDelete)

	// Delete old files
	for _, file := range filesToDelete {
		if err := os.Remove(file); err != nil {
			return fmt.Errorf("failed to remove old log file %s: %w", file, err)
		}
	}

	return nil
}

// extractDateFromLogName extracts date from rotated log filename
func extractDateFromLogName(filename, nameWithoutExt, ext string) string {
	// Remove prefix (e.g., "daemon-")
	suffix := strings.TrimPrefix(filename, nameWithoutExt+"-")

	// Remove extension and sequence number if present
	// Pattern could be: 2006-01-02.log or 2006-01-02.log.1
	if strings.HasSuffix(suffix, ext) {
		suffix = strings.TrimSuffix(suffix, ext)
		// Check if there's a sequence number after the extension
		parts := strings.Split(suffix, ".")
		if len(parts) > 0 {
			return parts[0] // Return just the date part
		}
	}

	return ""
}

func (l *logFile) readCreationTime() (time.Time, error) {
	// Open file for reading (separate from our write handle)
	file, err := os.Open(l.name)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to open log file for reading: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	// Read first line efficiently
	buf := make([]byte, 64) // RFC3339 is 25 chars, so 64 should be plenty
	n, err := file.Read(buf)
	if err != nil && err != io.EOF {
		return time.Time{}, fmt.Errorf("failed to read from log file: %w", err)
	}

	if n == 0 {
		return time.Time{}, fmt.Errorf("log file is empty")
	}

	// Find the first newline
	firstLine := string(buf[:n])
	if newlineIdx := strings.Index(firstLine, "\n"); newlineIdx != -1 {
		firstLine = firstLine[:newlineIdx]
	}

	// Parse the RFC3339 timestamp
	creationTime, err := time.Parse(time.RFC3339, strings.TrimSpace(firstLine))
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse creation timestamp '%s': %w", firstLine, err)
	}

	return creationTime, nil
}

func logHeader() []byte {
	return []byte(time.Now().Format(time.RFC3339) + "\n")
}
