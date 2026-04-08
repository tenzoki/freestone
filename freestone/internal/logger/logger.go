// Package logger provides a structured slog.Logger that writes JSON to a file
// and human-readable text to stderr.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
)

// New creates a multi-handler logger. JSON goes to logFile, text goes to stderr.
// If logFile is empty, only stderr output is produced.
// The caller must call Close on the returned io.Closer when done.
func New(logFile string) (*slog.Logger, io.Closer, error) {
	var handlers []slog.Handler

	stderrHandler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	handlers = append(handlers, stderrHandler)

	var fileCloser io.Closer = io.NopCloser(nil)

	if logFile != "" {
		f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("logger: open %s: %w", logFile, err)
		}
		fileCloser = f
		fileHandler := slog.NewJSONHandler(f, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
		handlers = append(handlers, fileHandler)
	}

	logger := slog.New(&multiHandler{handlers: handlers})
	return logger, fileCloser, nil
}

// multiHandler fans out log records to multiple handlers.
type multiHandler struct {
	handlers []slog.Handler
}

func (m *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, h := range m.handlers {
		if h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (m *multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m.handlers {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		cloned[i] = h.WithAttrs(attrs)
	}
	return &multiHandler{handlers: cloned}
}

func (m *multiHandler) WithGroup(name string) slog.Handler {
	cloned := make([]slog.Handler, len(m.handlers))
	for i, h := range m.handlers {
		cloned[i] = h.WithGroup(name)
	}
	return &multiHandler{handlers: cloned}
}
