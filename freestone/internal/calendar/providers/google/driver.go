// Package google implements the calendar.Provider interface using the
// Google Calendar API v3.
package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	domain "github.com/kai/freestone/internal/calendar"
)

// Driver implements calendar.Provider for Google Calendar.
type Driver struct {
	svc *calendar.Service
}

// New creates a Google Calendar driver using the given token source.
func New(ctx context.Context, ts oauth2.TokenSource) (*Driver, error) {
	svc, err := calendar.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("google calendar: create service: %w", err)
	}
	return &Driver{svc: svc}, nil
}

// Name returns "google".
func (d *Driver) Name() string { return "google" }

// ListCalendars returns all calendars the user has access to.
func (d *Driver) ListCalendars(ctx context.Context) ([]domain.Calendar, error) {
	list, err := d.svc.CalendarList.List().Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar: list calendars: %w", err)
	}

	cals := make([]domain.Calendar, 0, len(list.Items))
	for _, item := range list.Items {
		cals = append(cals, domain.Calendar{
			Provider:    "google",
			ID:          item.Id,
			Name:        item.Summary,
			Description: item.Description,
			TimeZone:    item.TimeZone,
		})
	}
	return cals, nil
}

// ListEvents returns events in the given calendar within the query time range.
// Uses SingleEvents(true) so the Google API expands recurring events.
// It reports no warnings: an item that cannot be converted fails the list.
func (d *Driver) ListEvents(ctx context.Context, q domain.EventQuery) (domain.EventList, error) {
	call := d.svc.Events.List(q.CalendarID).
		Context(ctx).
		SingleEvents(true).
		OrderBy("startTime")

	// The window is sent as the UTC instant: a layout with a literal "Z"
	// applied to a time carrying an offset would relabel its wall clock
	// as UTC and shift the window by that offset.
	if !q.Start.IsZero() {
		call = call.TimeMin(q.Start.UTC().Format(time.RFC3339))
	}
	if !q.End.IsZero() {
		call = call.TimeMax(q.End.UTC().Format(time.RFC3339))
	}

	var events []domain.Event
	err := call.Pages(ctx, func(page *calendar.Events) error {
		for _, item := range page.Items {
			ev, err := eventFromGoogle(item)
			if err != nil {
				return err
			}
			ev.CalendarID = q.CalendarID
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return domain.EventList{}, fmt.Errorf("google calendar: list events: %w", err)
	}
	return domain.EventList{Events: events}, nil
}

// GetEvent retrieves a single event by ID.
func (d *Driver) GetEvent(ctx context.Context, calendarID, uid string) (*domain.Event, error) {
	item, err := d.svc.Events.Get(calendarID, uid).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar: get event %s: %w", uid, err)
	}
	ev, err := eventFromGoogle(item)
	if err != nil {
		return nil, err
	}
	ev.CalendarID = calendarID
	return &ev, nil
}

// CreateEvent inserts a new event into the given calendar.
func (d *Driver) CreateEvent(ctx context.Context, calendarID string, e domain.Event) (*domain.Event, error) {
	ge := domainToGoogle(e)
	created, err := d.svc.Events.Insert(calendarID, ge).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar: create event: %w", err)
	}
	result, err := eventFromGoogle(created)
	if err != nil {
		return nil, err
	}
	result.CalendarID = calendarID
	return &result, nil
}

// UpdateEvent applies the patch with Events.Patch, so Google changes only
// the fields the patch sets and keeps everything else (recurrence,
// reminders, attendees, conference data). A patch carrying an etag is
// conditional: the PATCH sends it as If-Match, and Google's 412 becomes
// domain.ErrConflict.
func (d *Driver) UpdateEvent(ctx context.Context, p domain.EventPatch) (*domain.Event, error) {
	if p.CalendarID == "" {
		return nil, fmt.Errorf("google calendar: update requires calendar_id")
	}
	if p.UID == "" {
		return nil, fmt.Errorf("google calendar: update requires uid")
	}
	allDay := false
	if p.Start != nil || p.End != nil {
		// The patch carries no all-day flag, and a dateTime sent to an
		// all-day event turns it into a timed one, so a patch that moves
		// times first reads how the stored event is dated. A patch that
		// changes no times needs no read.
		stored, err := d.svc.Events.Get(p.CalendarID, p.UID).Context(ctx).Do()
		if err != nil {
			return nil, fmt.Errorf("google calendar: update event %s: read stored event: %w", p.UID, err)
		}
		allDay = stored.Start != nil && stored.Start.Date != ""
	}
	body, err := patchToGoogle(p, allDay)
	if err != nil {
		return nil, fmt.Errorf("google calendar: update event %s: %w", p.UID, err)
	}
	// The If-Match goes on the PATCH, not the read above: the read only
	// learns how the event is dated, and the write is what must not
	// overwrite a newer version.
	call := d.svc.Events.Patch(p.CalendarID, p.UID, body).Context(ctx)
	setIfMatch(call.Header(), p.ETag)
	updated, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar: update event %s: %w", p.UID, conflictOr(err))
	}
	result, err := eventFromGoogle(updated)
	if err != nil {
		return nil, err
	}
	result.CalendarID = p.CalendarID
	return &result, nil
}

// DeleteEvent removes an event from the given calendar. A non-empty etag
// makes the delete conditional on it, as for UpdateEvent.
func (d *Driver) DeleteEvent(ctx context.Context, calendarID, uid, etag string) error {
	call := d.svc.Events.Delete(calendarID, uid).Context(ctx)
	setIfMatch(call.Header(), etag)
	if err := call.Do(); err != nil {
		return fmt.Errorf("google calendar: delete event %s: %w", uid, conflictOr(err))
	}
	return nil
}

// setIfMatch makes a request conditional on etag, the domain's unquoted
// form, by sending it quoted as RFC 9110 §8.8.3 requires. An empty etag
// leaves the request unconditional.
func setIfMatch(h http.Header, etag string) {
	if etag != "" {
		h.Set("If-Match", strconv.Quote(etag))
	}
}

// conflictOr turns Google's 412 Precondition Failed into
// domain.ErrConflict and returns every other error unchanged.
func conflictOr(err error) error {
	var gerr *googleapi.Error
	if errors.As(err, &gerr) && gerr.Code == http.StatusPreconditionFailed {
		return fmt.Errorf("%w: %w", domain.ErrConflict, err)
	}
	return err
}
