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
	Start       string `json:"start" jsonschema:"Start time in RFC3339 format. For an all-day event, a date (YYYY-MM-DD) or RFC3339; the date is the one written, not converted to UTC."`
	End         string `json:"end" jsonschema:"End time in RFC3339 format. For an all-day event, the day after the last day (exclusive), as a date (YYYY-MM-DD) or RFC3339; it must be after start."`
	AllDay      bool   `json:"all_day,omitempty" jsonschema:"True for all-day events; start and end are then dates and end is exclusive."`
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
	Start       *string `json:"start,omitempty" jsonschema:"New start time in RFC3339 format. Give start and end together, or neither to keep the current times. On an all-day event (iCloud or Google) only the date on this value's own wall clock is used and the event stays all-day; end is then the exclusive end date and must fall on a later date."`
	End         *string `json:"end,omitempty" jsonschema:"New end time in RFC3339 format, after start. Give start and end together, or neither to keep the current times."`
	ETag        string  `json:"etag,omitempty" jsonschema:"The event's etag as last read, passed back exactly as received. When given, the update is made only if the event is unchanged since then; otherwise the call fails and the event must be re-fetched."`
	Scope       string  `json:"scope,omitempty" jsonschema:"iCloud only. Which part of a recurring event the update changes. iCloud lists every occurrence of a series under the series uid, so an update of a recurring iCloud event is refused unless scope is \"series\", which changes every occurrence (a time change moves the whole series, its moved and cancelled occurrences included). \"instance\" is refused: single-occurrence edits are not supported on iCloud. Not needed for a non-recurring event. Not accepted for google, where each occurrence has its own uid."`
}

type deleteEventInput struct {
	Provider     string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	CalendarID   string `json:"calendar_id" jsonschema:"Calendar ID"`
	UID          string `json:"uid" jsonschema:"Event UID to delete"`
	ETag         string `json:"etag,omitempty" jsonschema:"The event's etag as last read, passed back exactly as received. When given, the delete is made only if the event is unchanged since then; otherwise the call fails and the event must be re-fetched."`
	Scope        string `json:"scope,omitempty" jsonschema:"iCloud only. Which part of a recurring event the delete removes. iCloud lists every occurrence of a series under the series uid, so a delete of a recurring iCloud event is refused unless scope is given: \"series\" deletes every occurrence, \"instance\" deletes the one occurrence recurrence_id names. Not needed for a non-recurring event. Not accepted for google, where each occurrence has its own uid."`
	RecurrenceID string `json:"recurrence_id,omitempty" jsonschema:"iCloud only, required with scope \"instance\": the occurrence's recurrence_id exactly as calendar_list_events returns it (its original start, RFC3339). A value that is not an occurrence of the series is an error. Not accepted for google, where each occurrence has its own uid."`
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
			Description: "List events in a calendar within a time range. Returns {events, warnings}; warnings names each calendar object that could not be read and was left out. Each occurrence of an iCloud series carries the series uid and its own recurrence_id, which calendar_delete_event takes with scope \"instance\"; a Google occurrence has its own uid.",
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
			Description: "Create a new calendar event. For an all-day event set all_day and give start and end as dates (YYYY-MM-DD); end is exclusive, so a one-day event on 2026-11-26 ends 2026-11-27.",
		},
		withLog(log, "calendar_create_event", func(ctx context.Context, _ *mcp.CallToolRequest, input createEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			start, end, err := createEventTimes(input)
			if err != nil {
				return errResult(err), nil, nil
			}
			e := calendar.Event{
				Summary:     input.Summary,
				Description: input.Description,
				Location:    input.Location,
				StartUTC:    start,
				EndUTC:      end,
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
			Description: "Update an existing calendar event; only the fields you pass change. For summary, description and location: omit or null keeps the current value, an empty string clears it. Start and end are given together or not at all. Recurring events differ per provider. iCloud: an occurrence's uid is the series uid, so an update of a recurring event is refused unless scope is \"series\", which changes the whole series; one occurrence cannot be updated. Google: each occurrence has its own uid and the update changes only that occurrence; scope is not accepted.",
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
			Description: "Delete a calendar event. Recurring events differ per provider. iCloud: an occurrence's uid is the series uid, so a delete of a recurring event is refused unless scope is given: \"series\" deletes the whole series, \"instance\" with recurrence_id from calendar_list_events deletes one occurrence. Google: each occurrence has its own uid and the delete removes only that occurrence; scope and recurrence_id are not accepted.",
		},
		withLog(log, "calendar_delete_event", func(ctx context.Context, _ *mcp.CallToolRequest, input deleteEventInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			del, err := eventDeletionFromInput(input)
			if err != nil {
				return errResult(err), nil, nil
			}
			err = p.DeleteEvent(ctx, del)
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

// createEventTimes parses the start and end of a new event. Timed events
// become UTC instants. All-day events become UTC midnight of the date on
// the caller's own wall clock, the convention both decoders use: converting
// to UTC first would move 2026-11-26T00:00:00+01:00 to the 25th.
func createEventTimes(input createEventInput) (start, end time.Time, err error) {
	parse := func(s string) (time.Time, error) {
		t, err := time.Parse(time.RFC3339, s)
		return t.UTC(), err
	}
	if input.AllDay {
		parse = parseAllDayDate
	}
	if start, err = parse(input.Start); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid start: %w", err)
	}
	if end, err = parse(input.End); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid end: %w", err)
	}
	if input.AllDay && !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("all-day end %s must be after start %s; end is exclusive, so a one-day event ends the next day", input.End, input.Start)
	}
	return start, end, nil
}

// parseAllDayDate accepts YYYY-MM-DD or RFC3339 and returns the date as UTC
// midnight, taken from the input's own wall clock.
func parseAllDayDate(s string) (time.Time, error) {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		var rfcErr error
		if t, rfcErr = time.Parse(time.RFC3339, s); rfcErr != nil {
			return time.Time{}, fmt.Errorf("%q is neither YYYY-MM-DD nor RFC3339", s)
		}
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), nil
}

// eventPatchFromInput turns the tool input into a domain patch. Start and
// end move together: a lone start or end would need the stored duration,
// which the tool layer does not have.
func eventPatchFromInput(input updateEventInput) (calendar.EventPatch, error) {
	scope, err := calendar.ParseScope(input.Scope)
	if err != nil {
		return calendar.EventPatch{}, err
	}
	patch := calendar.EventPatch{
		CalendarID:  input.CalendarID,
		UID:         input.UID,
		Summary:     input.Summary,
		Description: input.Description,
		Location:    input.Location,
		ETag:        input.ETag,
		Scope:       scope,
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

// eventDeletionFromInput turns the tool input into a domain deletion. The
// recurrence_id is parsed here like every other time input; whether the
// provider accepts it, and with which scope, is the provider's to decide.
func eventDeletionFromInput(input deleteEventInput) (calendar.EventDeletion, error) {
	scope, err := calendar.ParseScope(input.Scope)
	if err != nil {
		return calendar.EventDeletion{}, err
	}
	del := calendar.EventDeletion{
		CalendarID: input.CalendarID,
		UID:        input.UID,
		ETag:       input.ETag,
		Scope:      scope,
	}
	if input.RecurrenceID != "" {
		if del.RecurrenceID, err = time.Parse(time.RFC3339, input.RecurrenceID); err != nil {
			return calendar.EventDeletion{}, fmt.Errorf("invalid recurrence_id: %w", err)
		}
	}
	return del, nil
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
