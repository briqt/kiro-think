package logutil

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// RotateWriter is a size-based log file rotator.
// When the file exceeds MaxSize bytes, it rotates: .log -> .log.1 -> .log.2 (up to MaxBackups).
type RotateWriter struct {
	path       string
	maxSize    int64
	maxBackups int

	mu   sync.Mutex
	file *os.File
	size int64
}

// NewRotateWriter creates a rotating log writer.
// maxSize in bytes, maxBackups is how many old files to keep.
func NewRotateWriter(path string, maxSize int64, maxBackups int) (*RotateWriter, error) {
	w := &RotateWriter{path: path, maxSize: maxSize, maxBackups: maxBackups}
	if err := w.openFile(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotateWriter) openFile() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.file = f
	w.size = info.Size()
	return nil
}

func (w *RotateWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.size+int64(len(p)) > w.maxSize {
		w.rotate()
	}
	n, err := w.file.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotateWriter) rotate() {
	w.file.Close()
	// Shift old files: .log.2 -> .log.3, .log.1 -> .log.2, .log -> .log.1
	for i := w.maxBackups - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
	}
	os.Rename(w.path, w.path+".1")
	// Remove excess
	os.Remove(fmt.Sprintf("%s.%d", w.path, w.maxBackups+1))
	w.openFile()
}

func (w *RotateWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

// Setup initializes slog with a rotating file writer.
// Returns the writer (for daemon stdout/stderr redirect) and a cleanup func.
func Setup(logFile string) (io.Writer, func()) {
	if logFile == "" || logFile == os.DevNull {
		return os.Stderr, func() {}
	}

	// 5MB max, keep 3 backups
	rw, err := NewRotateWriter(logFile, 5*1024*1024, 3)
	if err != nil {
		slog.Warn("failed to open log file, using stderr", "error", err)
		return os.Stderr, func() {}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(rw, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
	return rw, func() { rw.Close() }
}
