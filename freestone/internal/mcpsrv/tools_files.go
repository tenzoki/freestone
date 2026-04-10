package mcpsrv

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/audit"
	"github.com/kai/freestone/internal/files"
)

type listDirInput struct {
	Path string `json:"path" jsonschema:"Absolute directory path"`
}

type readFileInput struct {
	Path     string `json:"path" jsonschema:"Absolute file path"`
	MaxBytes int64  `json:"max_bytes,omitempty" jsonschema:"Max bytes to read (default from config)"`
}

type statInput struct {
	Path string `json:"path" jsonschema:"Absolute path"`
}

type globInput struct {
	Pattern string `json:"pattern" jsonschema:"Glob pattern"`
}

type writeFileInput struct {
	Path      string `json:"path" jsonschema:"Absolute file path"`
	Content   string `json:"content" jsonschema:"File content (text)"`
	Overwrite bool   `json:"overwrite,omitempty" jsonschema:"Allow overwriting existing file"`
}

type makeDirInput struct {
	Path string `json:"path" jsonschema:"Absolute directory path"`
}

func registerFileTools(srv *mcp.Server, fs files.FileService, al *audit.Logger, log *slog.Logger) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_list_dir",
			Description: "List directory contents. Only paths allowed by the access rules are shown.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listDirInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			entries, err := fs.ListDir(ctx, input.Path)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(entries), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_read_file",
			Description: "Read file contents. Returns text or base64 depending on content.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input readFileInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			data, err := fs.ReadFile(ctx, input.Path, input.MaxBytes)
			if err != nil {
				return errResult(err), nil, nil
			}
			// Return as text if valid UTF-8 and reasonable, otherwise base64.
			if isTextContent(data) {
				return &mcp.CallToolResult{
					Content: []mcp.Content{
						&mcp.TextContent{Text: string(data)},
					},
				}, nil, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: base64.StdEncoding.EncodeToString(data)},
				},
			}, nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_stat",
			Description: "Get file or directory metadata.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input statInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			entry, err := fs.Stat(ctx, input.Path)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(entry), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_glob",
			Description: "Find files matching a glob pattern. Only allowed paths are returned.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input globInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			matches, err := fs.Glob(ctx, input.Pattern)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(matches), nil, nil
		},
	)

	// -- Phase 2 write tools --

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_write_file",
			Description: "Write text content to a file. Only paths allowed by access rules are writable.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input writeFileInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			err := fs.WriteFile(ctx, input.Path, []byte(input.Content), input.Overwrite)
			auditLog(al, "files_write_file", "", map[string]string{
				"path":      input.Path,
				"overwrite": fmt.Sprintf("%v", input.Overwrite),
				"size":      fmt.Sprintf("%d", len(input.Content)),
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(map[string]string{"status": "written", "path": input.Path}), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "files_make_dir",
			Description: "Create a directory (and any missing parents). Only allowed paths are accepted.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input makeDirInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			err := fs.MakeDir(ctx, input.Path)
			auditLog(al, "files_make_dir", "", map[string]string{
				"path": input.Path,
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(map[string]string{"status": "created", "path": input.Path}), nil, nil
		},
	)
}

// isTextContent returns true if data is valid UTF-8 with no NUL bytes.
func isTextContent(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, b := range data {
		if b == 0 {
			return false
		}
	}
	return true
}
