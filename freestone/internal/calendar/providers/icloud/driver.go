// Package icloud implements the calendar.Provider interface using CalDAV
// (iCloud Calendar).
package icloud

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/kai/freestone/internal/calendar"
)

// Driver is the iCloud CalDAV calendar provider.
type Driver struct {
	client  *caldav.Client
	homeSet string
	log     *slog.Logger
}

// New creates a new iCloud calendar driver. It discovers the principal
// and calendar home set during construction.
func New(ctx context.Context, endpoint, username, password string, log *slog.Logger) (*Driver, error) {
	httpClient := webdav.HTTPClientWithBasicAuth(
		&http.Client{Timeout: 30 * time.Second},
		username, password,
	)
	client, err := caldav.NewClient(httpClient, endpoint)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: create client: %w", err)
	}

	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: find principal: %w", err)
	}

	homeSet, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: find home set: %w", err)
	}

	return &Driver{client: client, homeSet: homeSet, log: log}, nil
}

func (d *Driver) Name() string { return "icloud" }

func (d *Driver) ListCalendars(ctx context.Context) ([]calendar.Calendar, error) {
	cals, err := d.client.FindCalendars(ctx, d.homeSet)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: list calendars: %w", err)
	}

	result := make([]calendar.Calendar, 0, len(cals))
	for _, c := range cals {
		result = append(result, calendar.Calendar{
			Provider:    "icloud",
			ID:          c.Path,
			Name:        c.Name,
			Description: c.Description,
		})
	}
	return result, nil
}

func (d *Driver) ListEvents(ctx context.Context, q calendar.EventQuery) ([]calendar.Event, error) {
	calPath := q.CalendarID
	if calPath == "" {
		calPath = d.homeSet
	}

	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{
			Name:     "VCALENDAR",
			AllProps: true,
			Comps: []caldav.CalendarCompRequest{
				{
					Name:     "VEVENT",
					AllProps: true,
				},
			},
		},
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{
				{
					Name:  "VEVENT",
					Start: q.Start,
					End:   q.End,
				},
			},
		},
	}

	objects, err := d.client.QueryCalendar(ctx, calPath, query)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: query: %w", err)
	}

	var events []calendar.Event
	for _, obj := range objects {
		if obj.Data == nil {
			continue
		}
		decoded, err := decodeEvents(obj.Data, obj.ETag, q.CalendarID, q.Start, q.End, d.log)
		if err != nil {
			d.log.Warn("skipping malformed calendar object", "path", calPath, "etag", obj.ETag, "err", err)
			continue
		}
		for _, e := range decoded {
			// Filter expanded RRULE instances to the query window [Start, End).
			if e.RecurringParentUID != "" {
				if !q.Start.IsZero() && e.StartUTC.Before(q.Start) {
					continue
				}
				if !q.End.IsZero() && !e.StartUTC.Before(q.End) {
					continue
				}
			}
			events = append(events, e)
		}
	}
	return events, nil
}

func (d *Driver) GetEvent(ctx context.Context, calendarID, uid string) (*calendar.Event, error) {
	path := calendarID + uid + ".ics"
	obj, err := d.client.GetCalendarObject(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: get event %s: %w", uid, err)
	}
	if obj.Data == nil {
		return nil, fmt.Errorf("icloud-calendar: event %s has no data", uid)
	}
	events, err := decodeEvents(obj.Data, obj.ETag, calendarID, time.Time{}, time.Time{}, d.log)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("icloud-calendar: no events decoded from %s", uid)
	}
	return &events[0], nil
}

func (d *Driver) CreateEvent(ctx context.Context, calendarID string, e calendar.Event) (*calendar.Event, error) {
	cal, uid, err := encodeEvent(e)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: encode event: %w", err)
	}

	path := eventObjectPath(calendarID, uid)
	obj, err := d.client.PutCalendarObject(ctx, path, cal, nil)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: create event: %w", err)
	}

	e.Provider = "icloud"
	e.CalendarID = calendarID
	e.UID = uid
	if obj != nil {
		e.ETag = obj.ETag
	}
	return &e, nil
}

func (d *Driver) UpdateEvent(ctx context.Context, e calendar.Event) (*calendar.Event, error) {
	if e.CalendarID == "" {
		return nil, fmt.Errorf("icloud-calendar: update requires calendar_id")
	}
	if e.UID == "" {
		return nil, fmt.Errorf("icloud-calendar: update requires uid")
	}

	cal, _, err := encodeEvent(e)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: encode event: %w", err)
	}

	path := eventObjectPath(e.CalendarID, e.UID)
	obj, err := d.client.PutCalendarObject(ctx, path, cal, nil)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: update event %s: %w", e.UID, err)
	}

	e.Provider = "icloud"
	if obj != nil {
		e.ETag = obj.ETag
	}
	return &e, nil
}

func (d *Driver) DeleteEvent(ctx context.Context, calendarID, uid, _ string) error {
	path := eventObjectPath(calendarID, uid)
	if err := d.client.RemoveAll(ctx, path, nil); err != nil {
		return fmt.Errorf("icloud-calendar: delete event %s: %w", uid, err)
	}
	return nil
}
