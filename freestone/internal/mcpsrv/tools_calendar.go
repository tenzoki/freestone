package mcpsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/audit"
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

type createEventInput struct {
	Provider    string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID  string `json:"calendar_id" jsonschema:"Calendar ID"`
	Summary     string `json:"summary" jsonschema:"Event title"`
	Description string `json:"description,omitempty" jsonschema:"Event description"`
	Location    string `json:"location,omitempty" jsonschema:"Event location"`
	Start       string `json:"start" jsonschema:"Start time in RFC3339 format"`
	End         string `json:"end" jsonschema:"End time in RFC3339 format"`
	AllDay      bool   `json:"all_day,omitempty" jsonschema:"True for all-day events"`
}

type updateEventInput struct {
	Provider    string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID  string `json:"calendar_id" jsonschema:"Calendar ID"`
	UID         string `json:"uid" jsonschema:"Event UID to update"`
	Summary     string `json:"summary,omitempty" jsonschema:"Event title"`
	Description string `json:"description,omitempty" jsonschema:"Event description"`
	Location    string `json:"location,omitempty" jsonschema:"Event location"`
	Start       string `json:"start,omitempty" jsonschema:"Start time in RFC3339 format"`
	End         string `json:"end,omitempty" jsonschema:"End time in RFC3339 format"`
	ETag        string `json:"etag,omitempty" jsonschema:"ETag for conflict detection (iCloud only)"`
}

type deleteEventInput struct {
	Provider   string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID string `json:"calendar_id" jsonschema:"Calendar ID"`
	UID        string `json:"uid" jsonschema:"Event UID to delete"`
	ETag       string `json:"etag,omitempty" jsonschema:"ETag for conflict detection (iCloud only)"`
}

func registerCalendarTools(srv *mcp.Server, reg *calendar.Registry, al *audit.Logger, log *slog.Logger) {
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

	// -- Phase 2 write tools --

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_create_event",
			Description: "Create a new calendar event.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input createEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			start, err := time.Parse(time.RFC3339, input.Start)
			if err != nil {
				return errResult(fmt.Errorf("invalid start: %w", err)), nil, nil
			}
			end, err := time.Parse(time.RFC3339, input.End)
			if err != nil {
				return errResult(fmt.Errorf("invalid end: %w", err)), nil, nil
			}
			e := calendar.Event{
				Summary:     input.Summary,
				Description: input.Description,
				Location:    input.Location,
				StartUTC:    start.UTC(),
				EndUTC:      end.UTC(),
				AllDay:      input.AllDay,
			}
			created, err := p.CreateEvent(ctx, input.CalendarID, e)
			auditLog(al, "calendar_create_event", input.Provider, map[string]string{
				"calendar_id": input.CalendarID,
				"summary":     input.Summary,
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(created), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_update_event",
			Description: "Update an existing calendar event.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input updateEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			e := calendar.Event{
				CalendarID:  input.CalendarID,
				UID:         input.UID,
				Summary:     input.Summary,
				Description: input.Description,
				Location:    input.Location,
				ETag:        input.ETag,
			}
			if input.Start != "" {
				start, parseErr := time.Parse(time.RFC3339, input.Start)
				if parseErr != nil {
					return errResult(fmt.Errorf("invalid start: %w", parseErr)), nil, nil
				}
				e.StartUTC = start.UTC()
			}
			if input.End != "" {
				end, parseErr := time.Parse(time.RFC3339, input.End)
				if parseErr != nil {
					return errResult(fmt.Errorf("invalid end: %w", parseErr)), nil, nil
				}
				e.EndUTC = end.UTC()
			}
			updated, err := p.UpdateEvent(ctx, e)
			auditLog(al, "calendar_update_event", input.Provider, map[string]string{
				"calendar_id": input.CalendarID,
				"uid":         input.UID,
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(updated), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_delete_event",
			Description: "Delete a calendar event.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input deleteEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			err = p.DeleteEvent(ctx, input.CalendarID, input.UID, input.ETag)
			auditLog(al, "calendar_delete_event", input.Provider, map[string]string{
				"calendar_id": input.CalendarID,
				"uid":         input.UID,
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(map[string]string{"status": "deleted", "uid": input.UID}), nil, nil
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
