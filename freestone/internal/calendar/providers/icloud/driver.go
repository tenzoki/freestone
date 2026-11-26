// Package icloud implements the calendar.Provider interface using CalDAV
// (iCloud Calendar).
package icloud

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"

	"github.com/kai/freestone/internal/calendar"
)

// Driver is the iCloud CalDAV calendar provider.
type Driver struct {
	client  *caldav.Client
	homeSet string
	// floating is the location a floating date-time (no Z, no TZID) is
	// read in: RFC 5545 defines it as the user's local time, which only
	// the caller knows.
	floating *time.Location
	log      *slog.Logger
}

// New creates a new iCloud calendar driver. It discovers the principal
// and calendar home set during construction. floating is the location
// floating date-times are interpreted in.
func New(ctx context.Context, endpoint, username, password string, floating *time.Location, log *slog.Logger) (*Driver, error) {
	if floating == nil {
		return nil, fmt.Errorf("icloud-calendar: no location for floating times")
	}
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

	return &Driver{client: client, homeSet: homeSet, floating: floating, log: log}, nil
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

// ListEvents returns the events of every calendar object in the window. An
// object that cannot be decoded or expanded is skipped, logged, and named in
// the result's Warnings.
func (d *Driver) ListEvents(ctx context.Context, q calendar.EventQuery) (calendar.EventList, error) {
	calPath := q.CalendarID
	if calPath == "" {
		calPath = d.homeSet
	}

	// No CompRequest: a bare calendar-data returns each object whole. iCloud
	// answers a partial-retrieval request (comp + allprop) with VEVENTs
	// stripped of every property.
	query := &caldav.CalendarQuery{
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
		return calendar.EventList{}, fmt.Errorf("icloud-calendar: query: %w", err)
	}

	var list calendar.EventList
	skip := func(obj caldav.CalendarObject, err error) {
		d.log.Warn("skipping calendar object", "path", obj.Path, "etag", obj.ETag, "err", err)
		list.Warnings = append(list.Warnings, fmt.Sprintf("skipped %s: %v", obj.Path, err))
	}
	for _, obj := range objects {
		if obj.Data == nil {
			skip(obj, fmt.Errorf("no calendar data"))
			continue
		}
		decoded, err := decodeEvents(obj.Data, obj.ETag, q.CalendarID, q.Start, q.End, d.floating)
		if err != nil {
			skip(obj, err)
			continue
		}
		for _, e := range decoded {
			// Filter every recurring-series event, overrides included, to the
			// query window [Start, End): the server matched the object, not
			// each instance, and a moved instance may lie outside it.
			if e.RecurringParentUID != "" {
				if !q.Start.IsZero() && e.StartUTC.Before(q.Start) {
					continue
				}
				if !q.End.IsZero() && !e.StartUTC.Before(q.End) {
					continue
				}
			}
			list.Events = append(list.Events, e)
		}
	}
	return list, nil
}

func (d *Driver) GetEvent(ctx context.Context, calendarID, uid string) (*calendar.Event, error) {
	obj, err := d.loadObject(ctx, calendarID, uid)
	if err != nil {
		return nil, err
	}
	events, err := decodeEvents(obj.Data, obj.ETag, calendarID, time.Time{}, time.Time{}, d.floating)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("icloud-calendar: no events decoded from %s", uid)
	}
	return &events[0], nil
}

// loadObject finds the whole calendar object that holds the event uid. It
// is the one place Get, Update and Delete find an event's object.
//
// The object is looked up by UID, not at <calendar>/<uid>.ics: RFC 4791
// lets every client choose its own href, so an event another client wrote
// can live anywhere in the collection.
//
// It must always return the full object: Update edits it in place and
// writes it back, so anything a partial fetch left out would be deleted.
// Hence no CompRequest, so the server returns each object whole; iCloud
// answers a partial-retrieval request with VEVENTs stripped of every
// property.
func (d *Driver) loadObject(ctx context.Context, calendarID, uid string) (*caldav.CalendarObject, error) {
	query := &caldav.CalendarQuery{
		CompFilter: caldav.CompFilter{
			Name: "VCALENDAR",
			Comps: []caldav.CompFilter{{
				Name: "VEVENT",
				Props: []caldav.PropFilter{{
					Name:      "UID",
					TextMatch: &caldav.TextMatch{Text: uid},
				}},
			}},
		},
	}
	objects, err := d.client.QueryCalendar(ctx, calendarID, query)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: find event %s: %w", uid, err)
	}

	// text-match is a substring test on the server, so uid-10 answers a
	// query for uid-1; keep only exact UIDs.
	var matches []caldav.CalendarObject
	for _, obj := range objects {
		if obj.Data == nil {
			return nil, fmt.Errorf("icloud-calendar: event %s: object %s has no data", uid, obj.Path)
		}
		if holdsUID(obj.Data, uid) {
			matches = append(matches, obj)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("icloud-calendar: event %s not found in %s", uid, calendarID)
	case 1:
		return &matches[0], nil
	default:
		hrefs := make([]string, len(matches))
		for i, m := range matches {
			hrefs[i] = m.Path
		}
		return nil, fmt.Errorf("icloud-calendar: event %s is held by %d objects (%s); refusing to pick one", uid, len(matches), strings.Join(hrefs, ", "))
	}
}

// holdsUID reports whether any VEVENT in cal carries exactly uid.
func holdsUID(cal *ical.Calendar, uid string) bool {
	for _, c := range cal.Children {
		if c.Name != ical.CompEvent {
			continue
		}
		if p := c.Props.Get(ical.PropUID); p != nil && p.Value == uid {
			return true
		}
	}
	return false
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

// UpdateEvent applies the patch to the stored object rather than
// re-encoding the event from the domain struct, and writes it back only if
// it is still the version that was read.
func (d *Driver) UpdateEvent(ctx context.Context, p calendar.EventPatch) (*calendar.Event, error) {
	if p.CalendarID == "" {
		return nil, fmt.Errorf("icloud-calendar: update requires calendar_id")
	}
	if p.UID == "" {
		return nil, fmt.Errorf("icloud-calendar: update requires uid")
	}

	obj, err := d.loadObject(ctx, p.CalendarID, p.UID)
	if err != nil {
		return nil, err
	}
	master, err := applyPatch(obj.Data, p, time.Now())
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: update event %s: %w", p.UID, err)
	}

	// go-webdav returns ETags unquoted and sends If-Match verbatim, so the
	// entity tag is quoted here (RFC 9110 §8.8.3).
	opts := &caldav.PutCalendarObjectOptions{IfMatch: webdav.ConditionalMatch(strconv.Quote(obj.ETag))}
	put, err := d.client.PutCalendarObject(ctx, obj.Path, obj.Data, opts)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: update event %s: %w", p.UID, err)
	}

	e, err := decodeVEvent(master, put.ETag, p.CalendarID, d.floating)
	if err != nil {
		return nil, fmt.Errorf("icloud-calendar: decode updated event %s: %w", p.UID, err)
	}
	return &e, nil
}

func (d *Driver) DeleteEvent(ctx context.Context, calendarID, uid, _ string) error {
	obj, err := d.loadObject(ctx, calendarID, uid)
	if err != nil {
		return err
	}
	if err := d.client.RemoveAll(ctx, obj.Path, nil); err != nil {
		return fmt.Errorf("icloud-calendar: delete event %s: %w", uid, err)
	}
	return nil
}
