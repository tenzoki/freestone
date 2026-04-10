package mcpsrv

import (
	"context"
	"encoding/base64"
	"log/slog"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

func registerFileTools(srv *mcp.Server, fs files.FileService, log *slog.Logger) {
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
