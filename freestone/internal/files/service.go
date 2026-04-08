// Package files provides sandboxed read/write access to the local filesystem.
// Access is governed by a rules file with ordered allow/deny rules.
package files

import (
	"context"
	"time"
)

// FileEntry describes a single file or directory.
type FileEntry struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
}

// FileService provides sandboxed file operations.
type FileService interface {
	ListDir(ctx context.Context, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, path string, maxBytes int64) ([]byte, error)
	Stat(ctx context.Context, path string) (*FileEntry, error)
	Glob(ctx context.Context, pattern string) ([]string, error)
	// Phase 2
	WriteFile(ctx context.Context, path string, data []byte, overwrite bool) error
	MakeDir(ctx context.Context, path string) error
}
