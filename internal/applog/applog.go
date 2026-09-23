// Package applog writes the application's diagnostic log.
//
// The log lives in the platform's per-user state directory rather than wherever the binary
// happened to be started, keeps a single file handle open, and rotates to one backup when it
// grows past MaxSize. Until Open is called every write is dropped, so tests and tools that
// link the network code never leave log files behind.
package applog

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// MaxSize is the size at which the log is rotated to <name>.1, replacing any older backup.
const MaxSize = 5 << 20

const fileName = "limoni-voice.log"

var (
	mu   sync.Mutex
	file *os.File
	path string
	size int64
)

// DefaultPath returns where the log is written unless LIMONI_LOG_FILE names another file:
//
//	Linux    $XDG_STATE_HOME/limoni-voice (~/.local/state/limoni-voice)
//	macOS    ~/Library/Logs/limoni-voice
//	Windows  %LOCALAPPDATA%\limoni-voice\logs
func DefaultPath() string {
	if p := os.Getenv("LIMONI_LOG_FILE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	var dir string
	switch runtime.GOOS {
	case "darwin":
		dir = filepath.Join(home, "Library", "Logs", "limoni-voice")
	case "windows":
		base, err := os.UserCacheDir() // %LOCALAPPDATA%
		if err != nil {
			base = home
		}
		dir = filepath.Join(base, "limoni-voice", "logs")
	default:
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			base = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(base, "limoni-voice")
	}
	return filepath.Join(dir, fileName)
}

// Open starts writing the log to p, creating its directory. It is safe to call again to
// move the log elsewhere.
func Open(p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
	}
	file, path, size = f, p, st.Size()
	return nil
}

// Path returns the file being written, or "" before Open.
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Close stops writing the log.
func Close() {
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
}

// Print writes one timestamped line.
func Print(msg string) {
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return
	}
	line := time.Now().Format("2006-01-02 15:04:05.000 ") + msg + "\n"
	if size+int64(len(line)) > MaxSize {
		rotateLocked()
		if file == nil {
			return
		}
	}
	n, _ := file.WriteString(line)
	size += int64(n)
}

// Printf formats and writes one timestamped line.
func Printf(format string, args ...any) {
	Print(fmt.Sprintf(format, args...))
}

// rotateLocked moves the current file to <path>.1 and starts an empty one.
func rotateLocked() {
	file.Close()
	file = nil
	_ = os.Rename(path, path+".1")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	file, size = f, 0
}
