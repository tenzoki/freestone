// Package google implements the calendar.Provider interface using the
// Google Calendar API v3.
package google

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
	"google.golang.org/api/calendar/v3"
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
func (d *Driver) ListEvents(ctx context.Context, q domain.EventQuery) ([]domain.Event, error) {
	call := d.svc.Events.List(q.CalendarID).
		Context(ctx).
		SingleEvents(true).
		OrderBy("startTime")

	if !q.Start.IsZero() {
		call = call.TimeMin(q.Start.Format("2006-01-02T15:04:05Z"))
	}
	if !q.End.IsZero() {
		call = call.TimeMax(q.End.Format("2006-01-02T15:04:05Z"))
	}

	var events []domain.Event
	err := call.Pages(ctx, func(page *calendar.Events) error {
		for _, item := range page.Items {
			ev, err := eventFromGoogle(item)
			if err != nil {
				return err
			}
			events = append(events, ev)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("google calendar: list events: %w", err)
	}
	return events, nil
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
// reminders, attendees, conference data). Google Calendar does not use
// ETags for conflict detection via this client, so the patch's ETag is
// accepted but not enforced.
func (d *Driver) UpdateEvent(ctx context.Context, p domain.EventPatch) (*domain.Event, error) {
	if p.CalendarID == "" {
		return nil, fmt.Errorf("google calendar: update requires calendar_id")
	}
	if p.UID == "" {
		return nil, fmt.Errorf("google calendar: update requires uid")
	}
	updated, err := d.svc.Events.Patch(p.CalendarID, p.UID, patchToGoogle(p)).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("google calendar: update event %s: %w", p.UID, err)
	}
	result, err := eventFromGoogle(updated)
	if err != nil {
		return nil, err
	}
	result.CalendarID = p.CalendarID
	return &result, nil
}

// DeleteEvent removes an event from the given calendar.
// The etag parameter is accepted for interface compatibility but is not
// used by the Google Calendar API client.
func (d *Driver) DeleteEvent(ctx context.Context, calendarID, uid, _ string) error {
	if err := d.svc.Events.Delete(calendarID, uid).Context(ctx).Do(); err != nil {
		return fmt.Errorf("google calendar: delete event %s: %w", uid, err)
	}
	return nil
}
