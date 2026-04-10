package mcpsrv

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/audit"
)

// withLog wraps a tool handler to log every invocation with tool name and duration.
func withLog[T any](log *slog.Logger, name string,
	fn func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error),
) func(context.Context, *mcp.CallToolRequest, T) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input T) (*mcp.CallToolResult, any, error) {
		start := time.Now()
		res, meta, err := fn(ctx, req, input)
		dur := time.Since(start)
		if res != nil && res.IsError {
			log.Warn("tool call failed", slog.String("tool", name), slog.Duration("duration", dur))
		} else {
			log.Info("tool call", slog.String("tool", name), slog.Duration("duration", dur))
		}
		return res, meta, err
	}
}

// auditLog records a write tool invocation. Called after the tool handler
// returns so that both success and failure are captured.
func auditLog(logger *audit.Logger, tool, provider string, inputs map[string]string, err error) {
	if logger == nil {
		return
	}
	rec := audit.Record{
		Time:     time.Now().UTC(),
		Tool:     tool,
		Provider: provider,
		Inputs:   inputs,
		Outcome:  "ok",
	}
	if err != nil {
		rec.Outcome = "error"
		rec.Error = err.Error()
	}
	// Best-effort: audit log write failures are not surfaced to the caller.
	logger.Log(rec)
}
