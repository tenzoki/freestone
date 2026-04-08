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

// -- Phase 2 write stubs --

func (d *Driver) CreateEvent(_ context.Context, _ string, _ domain.Event) (*domain.Event, error) {
	return nil, fmt.Errorf("google calendar: create not implemented (phase 2)")
}

func (d *Driver) UpdateEvent(_ context.Context, _ domain.Event) (*domain.Event, error) {
	return nil, fmt.Errorf("google calendar: update not implemented (phase 2)")
}

func (d *Driver) DeleteEvent(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("google calendar: delete not implemented (phase 2)")
}
