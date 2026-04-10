package files

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// LocalFS provides sandboxed filesystem access governed by a RuleSet.
type LocalFS struct {
	rules        *RuleSet
	maxReadBytes int64
	log          *slog.Logger
}

// NewLocalFS creates a new local filesystem service.
func NewLocalFS(rules *RuleSet, maxReadBytes int64, log *slog.Logger) *LocalFS {
	return &LocalFS{rules: rules, maxReadBytes: maxReadBytes, log: log}
}

func (fs *LocalFS) checkAccess(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("files: resolve path %q: %w", path, err)
	}
	// Resolve symlinks so the canonical path is checked.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// If the path doesn't exist, check the original absolute path.
		// This matters for Glob where we check patterns.
		if os.IsNotExist(err) {
			resolved = abs
		} else {
			return "", fmt.Errorf("files: resolve symlinks %q: %w", abs, err)
		}
	}
	if !fs.rules.Allowed(resolved) {
		return "", fmt.Errorf("files: access denied: %s", path)
	}
	return resolved, nil
}

func (fs *LocalFS) ListDir(_ context.Context, path string) ([]FileEntry, error) {
	resolved, err := fs.checkAccess(path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: list %s: %w", path, err)
	}

	var result []FileEntry
	for _, e := range entries {
		childPath := filepath.Join(resolved, e.Name())
		// Only include children that are also allowed.
		if !fs.rules.Allowed(childPath) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			fs.log.Warn("skipping dir entry: stat failed", "path", childPath, "err", err)
			continue
		}
		result = append(result, FileEntry{
			Path:    filepath.Join(path, e.Name()),
			Name:    e.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return result, nil
}

func (fs *LocalFS) ReadFile(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	resolved, err := fs.checkAccess(path)
	if err != nil {
		return nil, err
	}

	limit := fs.maxReadBytes
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}

	f, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: open %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("files: read %s: %w", path, err)
	}

	if int64(len(data)) > limit {
		return data[:limit], nil
	}
	return data, nil
}

func (fs *LocalFS) Stat(_ context.Context, path string) (*FileEntry, error) {
	resolved, err := fs.checkAccess(path)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("files: stat %s: %w", path, err)
	}

	return &FileEntry{
		Path:    path,
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (fs *LocalFS) Glob(_ context.Context, pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("files: glob %q: %w", pattern, err)
	}

	// Filter results through rules so disallowed paths never leak.
	var allowed []string
	for _, m := range matches {
		abs, err := filepath.Abs(m)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			continue
		}
		if fs.rules.Allowed(resolved) {
			allowed = append(allowed, m)
		}
	}
	return allowed, nil
}

func (fs *LocalFS) WriteFile(_ context.Context, path string, data []byte, overwrite bool) error {
	resolved, err := fs.checkAccess(path)
	if err != nil {
		return err
	}

	if !overwrite {
		if _, statErr := os.Stat(resolved); statErr == nil {
			return fmt.Errorf("files: %s already exists (set overwrite=true to replace)", path)
		}
	}

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("files: create parent dirs for %s: %w", path, err)
	}

	if err := os.WriteFile(resolved, data, 0o644); err != nil {
		return fmt.Errorf("files: write %s: %w", path, err)
	}
	return nil
}

func (fs *LocalFS) MakeDir(_ context.Context, path string) error {
	resolved, err := fs.checkAccess(path)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(resolved, 0o755); err != nil {
		return fmt.Errorf("files: mkdir %s: %w", path, err)
	}
	return nil
}
