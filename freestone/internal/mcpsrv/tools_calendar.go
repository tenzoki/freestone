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

// updateEventInput uses pointers so that an omitted or null field can be
// told apart from an empty string: nil keeps the stored value, "" clears it.
type updateEventInput struct {
	Provider    string  `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID  string  `json:"calendar_id" jsonschema:"Calendar ID"`
	UID         string  `json:"uid" jsonschema:"Event UID to update"`
	Summary     *string `json:"summary,omitempty" jsonschema:"Event title. Omit or null keeps the current value, an empty string clears it."`
	Description *string `json:"description,omitempty" jsonschema:"Event description. Omit or null keeps the current value, an empty string clears it."`
	Location    *string `json:"location,omitempty" jsonschema:"Event location. Omit or null keeps the current value, an empty string clears it."`
	Start       *string `json:"start,omitempty" jsonschema:"New start time in RFC3339 format. Give start and end together, or neither to keep the current times."`
	End         *string `json:"end,omitempty" jsonschema:"New end time in RFC3339 format, after start. Give start and end together, or neither to keep the current times."`
	ETag        string  `json:"etag,omitempty" jsonschema:"ETag for conflict detection (iCloud only)"`
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
		withLog(log, "calendar_list_calendars", func(ctx context.Context, _ *mcp.CallToolRequest, input listCalendarsInput) (*mcp.CallToolResult, any, error) {
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
		}),
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_list_events",
			Description: "List events in a calendar within a time range. Returns {events, warnings}; warnings names each calendar object that could not be read and was left out.",
		},
		withLog(log, "calendar_list_events", func(ctx context.Context, _ *mcp.CallToolRequest, input listEventsInput) (*mcp.CallToolResult, any, error) {
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
			list, err := p.ListEvents(ctx, calendar.EventQuery{
				CalendarID: input.CalendarID,
				Start:      start,
				End:        end,
			})
			if err != nil {
				return errResult(err), nil, nil
			}
			// An empty window is an empty array, not null.
			if list.Events == nil {
				list.Events = []calendar.Event{}
			}
			return jsonResult(list), nil, nil
		}),
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_get_event",
			Description: "Get a single event by UID.",
		},
		withLog(log, "calendar_get_event", func(ctx context.Context, _ *mcp.CallToolRequest, input getEventInput) (*mcp.CallToolResult, any, error) {
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
		}),
	)

	// -- Phase 2 write tools --

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_create_event",
			Description: "Create a new calendar event.",
		},
		withLog(log, "calendar_create_event", func(ctx context.Context, _ *mcp.CallToolRequest, input createEventInput) (*mcp.CallToolResult, any, error) {
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
		}),
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_update_event",
			Description: "Update an existing calendar event; only the fields you pass change. For summary, description and location: omit or null keeps the current value, an empty string clears it. Start and end are given together or not at all.",
		},
		withLog(log, "calendar_update_event", func(ctx context.Context, _ *mcp.CallToolRequest, input updateEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			patch, err := eventPatchFromInput(input)
			if err != nil {
				return errResult(err), nil, nil
			}
			updated, err := p.UpdateEvent(ctx, patch)
			auditLog(al, "calendar_update_event", input.Provider, map[string]string{
				"calendar_id": input.CalendarID,
				"uid":         input.UID,
			}, err)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(updated), nil, nil
		}),
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "calendar_delete_event",
			Description: "Delete a calendar event.",
		},
		withLog(log, "calendar_delete_event", func(ctx context.Context, _ *mcp.CallToolRequest, input deleteEventInput) (*mcp.CallToolResult, any, error) {
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
		}),
	)
}

// eventPatchFromInput turns the tool input into a domain patch. Start and
// end move together: a lone start or end would need the stored duration,
// which the tool layer does not have.
func eventPatchFromInput(input updateEventInput) (calendar.EventPatch, error) {
	patch := calendar.EventPatch{
		CalendarID:  input.CalendarID,
		UID:         input.UID,
		Summary:     input.Summary,
		Description: input.Description,
		Location:    input.Location,
		ETag:        input.ETag,
	}
	if (input.Start == nil) != (input.End == nil) {
		return calendar.EventPatch{}, fmt.Errorf("start and end must be given together")
	}
	if input.Start == nil {
		return patch, nil
	}
	start, err := time.Parse(time.RFC3339, *input.Start)
	if err != nil {
		return calendar.EventPatch{}, fmt.Errorf("invalid start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, *input.End)
	if err != nil {
		return calendar.EventPatch{}, fmt.Errorf("invalid end: %w", err)
	}
	if !end.After(start) {
		return calendar.EventPatch{}, fmt.Errorf("end %s must be after start %s", *input.End, *input.Start)
	}
	patch.Start, patch.End = &start, &end
	return patch, nil
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
