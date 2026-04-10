package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/calendar"
)

// -- Input types --

type listCalendarsInput struct {
	Provider string `json:"provider,omitempty" jsonschema:"Provider name (icloud or google). Omit to list all."`
}

type listEventsInput struct {
	Provider   string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID from list_calendars"`
	Start      string `json:"start" jsonschema:"Start time in RFC3339 format"`
	End        string `json:"end" jsonschema:"End time in RFC3339 format"`
}

type getEventInput struct {
	Provider   string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID"`
	UID        string `json:"uid" jsonschema:"Event UID"`
}

func registerCalendarTools(srv *mcp.Server, reg *calendar.Registry, log *slog.Logger) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_list_calendars",
			Description: "List calendars. Omit provider to list from all enabled providers.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listCalendarsInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			if input.Provider == "" {
				cals, err := reg.ListAllCalendars(ctx)
				if err != nil {
					return errResult(err), nil, nil
				}
				return jsonResult(cals), nil, nil
			}
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			cals, err := p.ListCalendars(ctx)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(cals), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_list_events",
			Description: "List events in a calendar within a time range.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listEventsInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			start, err := time.Parse(time.RFC3339, input.Start)
			if err != nil {
				return errResult(fmt.Errorf("invalid start time: %w", err)), nil, nil
			}
			end, err := time.Parse(time.RFC3339, input.End)
			if err != nil {
				return errResult(fmt.Errorf("invalid end time: %w", err)), nil, nil
			}
			events, err := p.ListEvents(ctx, calendar.EventQuery{
				CalendarID: input.CalendarID,
				Start:      start,
				End:        end,
			})
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(events), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_get_event",
			Description: "Get a single event by UID.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input getEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			event, err := p.GetEvent(ctx, input.CalendarID, input.UID)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(event), nil, nil
		},
	)
}

// jsonResult serializes v as JSON text content.
func jsonResult(v any) *mcp.CallToolResult {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResult(err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}
}

// errResult creates an error tool result.
func errResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: err.Error()},
		},
		IsError: true,
	}
}
