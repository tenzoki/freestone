package files

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FakeFS is an in-memory FileService for testing.
type FakeFS struct {
	Files map[string][]byte    // path → content
	Dirs  map[string]bool      // paths that are directories
	Times map[string]time.Time // path → mod time
}

// NewFakeFS creates a FakeFS with sensible defaults.
func NewFakeFS() *FakeFS {
	return &FakeFS{
		Files: make(map[string][]byte),
		Dirs:  make(map[string]bool),
		Times: make(map[string]time.Time),
	}
}

func (f *FakeFS) ListDir(_ context.Context, path string) ([]FileEntry, error) {
	path = filepath.Clean(path)
	if !f.Dirs[path] {
		return nil, fmt.Errorf("fake: not a directory: %s", path)
	}

	var result []FileEntry
	prefix := path + "/"
	seen := make(map[string]bool)

	for p, data := range f.Files {
		if strings.HasPrefix(p, prefix) {
			rest := strings.TrimPrefix(p, prefix)
			name := strings.SplitN(rest, "/", 2)[0]
			if seen[name] {
				continue
			}
			seen[name] = true
			result = append(result, FileEntry{
				Path:    filepath.Join(path, name),
				Name:    name,
				Size:    int64(len(data)),
				Mode:    "-rw-r--r--",
				ModTime: f.modTime(p),
				IsDir:   false,
			})
		}
	}
	for p := range f.Dirs {
		if strings.HasPrefix(p, prefix) && p != path {
			rest := strings.TrimPrefix(p, prefix)
			name := strings.SplitN(rest, "/", 2)[0]
			childPath := filepath.Join(path, name)
			if seen[name] || childPath != p {
				continue
			}
			seen[name] = true
			result = append(result, FileEntry{
				Path:    childPath,
				Name:    name,
				Size:    0,
				Mode:    "drwxr-xr-x",
				ModTime: f.modTime(p),
				IsDir:   true,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (f *FakeFS) ReadFile(_ context.Context, path string, maxBytes int64) ([]byte, error) {
	path = filepath.Clean(path)
	data, ok := f.Files[path]
	if !ok {
		return nil, fmt.Errorf("fake: file not found: %s", path)
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return data[:maxBytes], nil
	}
	return data, nil
}

func (f *FakeFS) Stat(_ context.Context, path string) (*FileEntry, error) {
	path = filepath.Clean(path)
	if f.Dirs[path] {
		return &FileEntry{
			Path:    path,
			Name:    filepath.Base(path),
			IsDir:   true,
			Mode:    "drwxr-xr-x",
			ModTime: f.modTime(path),
		}, nil
	}
	if data, ok := f.Files[path]; ok {
		return &FileEntry{
			Path:    path,
			Name:    filepath.Base(path),
			Size:    int64(len(data)),
			Mode:    "-rw-r--r--",
			ModTime: f.modTime(path),
		}, nil
	}
	return nil, fmt.Errorf("fake: not found: %s", path)
}

func (f *FakeFS) Glob(_ context.Context, pattern string) ([]string, error) {
	var matches []string
	for p := range f.Files {
		matched, _ := filepath.Match(pattern, p)
		if matched {
			matches = append(matches, p)
		}
	}
	sort.Strings(matches)
	return matches, nil
}

func (f *FakeFS) WriteFile(_ context.Context, _ string, _ []byte, _ bool) error {
	return fmt.Errorf("fake: write not implemented in phase 1")
}

func (f *FakeFS) MakeDir(_ context.Context, _ string) error {
	return fmt.Errorf("fake: mkdir not implemented in phase 1")
}

func (f *FakeFS) modTime(path string) time.Time {
	if t, ok := f.Times[path]; ok {
		return t
	}
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
}
