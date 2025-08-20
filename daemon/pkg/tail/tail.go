package tail

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// File watches the specified file for changes, similar to 'tail -f', and streams
// new lines to a channel. It handles file rotation by detecting renames and re-opening
// the new file. The function stops when the context is canceled. The parameter n specifies
// the number of most recent lines to read initially (default 10). If n = -1, no initial
// lines are read, and only new lines are streamed. The returned channel receives lines
// as strings, and any error during watching is sent to the error channel.
func File(ctx context.Context, filePath string, n int) (<-chan string, <-chan error, error) {
	lineCh := make(chan string, 100) // Buffered to prevent blocking
	errCh := make(chan error, 1)

	// Initialize file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}

	// Resolve absolute path to handle renames
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		watcher.Close()
		return nil, nil, err
	}

	// Open the file
	file, err := os.Open(absPath)
	if err != nil {
		watcher.Close()
		return nil, nil, err
	}

	// Read initial lines if n >= 0
	if n >= 0 {
		if n == 0 {
			n = 10 // Default to 10 lines
		}
		lines, err := readLastLines(file, n)
		if err != nil {
			file.Close()
			watcher.Close()
			return nil, nil, err
		}
		// Send initial lines asynchronously
		go func() {
			for _, line := range lines {
				select {
				case lineCh <- line:
				case <-ctx.Done():
					file.Close()
					watcher.Close()
					return
				}
			}
		}()
	} else {
		// Seek to the end if n = -1
		_, err = file.Seek(0, io.SeekEnd)
		if err != nil {
			file.Close()
			watcher.Close()
			return nil, nil, err
		}
	}

	// Add file to watcher
	err = watcher.Add(absPath)
	if err != nil {
		file.Close()
		watcher.Close()
		return nil, nil, err
	}

	// Start goroutine to handle file watching and reading
	go func() {
		defer watcher.Close()
		defer close(lineCh)
		defer close(errCh)

		currentFile := file
		lastWrite := time.Now()

		for {
			select {
			case <-ctx.Done():
				currentFile.Close()
				return
			case event, ok := <-watcher.Events:
				if !ok {
					currentFile.Close()
					errCh <- fsnotify.ErrEventOverflow
					return
				}
				if event.Has(fsnotify.Write) {
					// Debounce rapid writes
					if time.Since(lastWrite) < 10*time.Millisecond {
						continue
					}
					lastWrite = time.Now()

					// Create new scanner to reset state
					scanner := bufio.NewScanner(currentFile)
					for scanner.Scan() {
						select {
						case lineCh <- scanner.Text():
						case <-ctx.Done():
							currentFile.Close()
							return
						}
					}
					if err := scanner.Err(); err != nil && err != io.EOF {
						currentFile.Close()
						errCh <- err
						return
					}
					// Seek to current position to ensure alignment
					_, err := currentFile.Seek(0, io.SeekCurrent)
					if err != nil {
						currentFile.Close()
						errCh <- err
						return
					}
				}
				if event.Has(fsnotify.Rename | fsnotify.Remove) {
					// Handle log rotation: re-open the new file
					currentFile.Close()
					newFile, err := os.Open(absPath)
					if err != nil {
						errCh <- err
						return
					}
					currentFile = newFile
					_, err = currentFile.Seek(0, io.SeekEnd)
					if err != nil {
						currentFile.Close()
						errCh <- err
						return
					}
					err = watcher.Add(absPath)
					if err != nil {
						currentFile.Close()
						errCh <- err
						return
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					currentFile.Close()
					return
				}
				errCh <- err
				return
			}
		}
	}()

	return lineCh, errCh, nil
}

// readLastLines reads the last n lines from the file. It assumes the file is already
// opened, seeks to the beginning to read, and restores the file position to the end.
func readLastLines(file *os.File, n int) ([]string, error) {
	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	// Seek back to the end
	_, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	return lines, nil
}
