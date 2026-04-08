// Package mcpsrv registers MCP tools and wires them to domain services.
// This is a thin adapter layer — no business logic lives here.
package mcpsrv

import (
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
	"github.com/kai/freestone/internal/files"
	"github.com/kai/freestone/internal/mail"
)

// Deps holds the domain services the MCP server depends on.
// Nil fields mean that domain is not enabled.
type Deps struct {
	Calendar *calendar.Registry
	Mail     *mail.Registry
	Files    files.FileService
	Log      *slog.Logger
}

// Build creates an MCP server and registers tools for all enabled domains.
func Build(deps Deps) *mcp.Server {
	srv := mcp.NewServer(
		&mcp.Implementation{
			Name:    "freestone",
			Version: "0.1.0",
		},
		nil,
	)

	if deps.Calendar != nil {
		registerCalendarTools(srv, deps.Calendar, deps.Log)
	}
	if deps.Mail != nil {
		registerMailTools(srv, deps.Mail, deps.Log)
	}
	if deps.Files != nil {
		registerFileTools(srv, deps.Files, deps.Log)
	}

	return srv
}
