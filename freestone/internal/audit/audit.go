// Package audit provides an append-only JSONL writer for recording
// write-operation audit trails.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Record captures a single audited tool invocation.
type Record struct {
	Time     time.Time         `json:"time"`
	Tool     string            `json:"tool"`
	Provider string            `json:"provider,omitempty"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Outcome  string            `json:"outcome"` // "ok" or "error"
	Error    string            `json:"error,omitempty"`
}

// Logger writes audit records as one JSON line per entry.
// It is safe for concurrent use.
type Logger struct {
	mu   sync.Mutex
	file *os.File
}

// New opens (or creates) the audit log file for append-only writing.
// The file is created with mode 0600 (owner read/write only).
func New(path string) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}
	return &Logger{file: f}, nil
}

// Log serializes the record as a single JSON line and appends it to the file.
// It never writes partial lines — a failed marshal returns an error without
// writing anything.
func (l *Logger) Log(rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("audit: marshal: %w", err)
	}
	data = append(data, '\n')

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.file.Write(data); err != nil {
		return fmt.Errorf("audit: write: %w", err)
	}
	return nil
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}
