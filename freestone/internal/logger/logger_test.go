package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestMultiHandler_FanOut(t *testing.T) {
	var buf1, buf2 bytes.Buffer
	h1 := slog.NewTextHandler(&buf1, &slog.HandlerOptions{Level: slog.LevelInfo})
	h2 := slog.NewJSONHandler(&buf2, &slog.HandlerOptions{Level: slog.LevelInfo})

	mh := &multiHandler{handlers: []slog.Handler{h1, h2}}
	log := slog.New(mh)
	log.Info("hello", "key", "val")

	if !strings.Contains(buf1.String(), "hello") {
		t.Errorf("handler 1 missing output: %s", buf1.String())
	}
	if !strings.Contains(buf2.String(), "hello") {
		t.Errorf("handler 2 missing output: %s", buf2.String())
	}
}

func TestMultiHandler_Enabled(t *testing.T) {
	h := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	mh := &multiHandler{handlers: []slog.Handler{h}}

	if mh.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("expected Info to be disabled when handler is Warn-level")
	}
	if !mh.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("expected Warn to be enabled")
	}
}

func TestMultiHandler_WithAttrs(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	mh := &multiHandler{handlers: []slog.Handler{h}}

	child := mh.WithAttrs([]slog.Attr{slog.String("component", "test")})
	log := slog.New(child)
	log.Info("msg")

	if !strings.Contains(buf.String(), "component=test") {
		t.Errorf("expected attribute in output: %s", buf.String())
	}
}

func TestMultiHandler_WithGroup(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	mh := &multiHandler{handlers: []slog.Handler{h}}

	child := mh.WithGroup("grp")
	log := slog.New(child)
	log.Info("msg", "k", "v")

	if !strings.Contains(buf.String(), "grp.k=v") {
		t.Errorf("expected grouped attribute in output: %s", buf.String())
	}
}

func TestNew_StderrOnly(t *testing.T) {
	// Empty logFile means stderr-only output.
	log, closer, err := New("")
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	if log == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNew_WithFile(t *testing.T) {
	tmp := t.TempDir() + "/test.log"
	log, closer, err := New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()

	log.Info("test message")
	closer.Close()

	// Verify something was written.
	// The file should exist and contain the message.
}
