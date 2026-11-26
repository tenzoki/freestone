package google

import (
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

// eventFromGoogle converts a Google Calendar event to a domain Event.
func eventFromGoogle(item *calendar.Event) (domain.Event, error) {
	ev := domain.Event{
		Provider:    "google",
		UID:         item.Id,
		Summary:     item.Summary,
		Description: item.Description,
		Location:    item.Location,
		Status:      strings.ToUpper(item.Status),
	}

	// Organizer is the address, as on iCloud and as the write path sends it;
	// the display name is not an identifier.
	if item.Organizer != nil {
		ev.Organizer = item.Organizer.Email
	}

	// Attendees
	for _, a := range item.Attendees {
		if a.Email != "" {
			ev.Attendees = append(ev.Attendees, a.Email)
		}
	}

	// Recurring event parent
	if item.RecurringEventId != "" {
		ev.RecurringParentUID = item.RecurringEventId
	}

	// Parse start time
	if item.Start != nil {
		if item.Start.Date != "" {
			// All-day event
			ev.AllDay = true
			t, err := time.Parse("2006-01-02", item.Start.Date)
			if err != nil {
				return domain.Event{}, fmt.Errorf("google calendar: parse start date %q: %w", item.Start.Date, err)
			}
			ev.StartUTC = t
			ev.StartLocal = item.Start.Date
			if item.Start.TimeZone != "" {
				ev.TZID = item.Start.TimeZone
			}
		} else if item.Start.DateTime != "" {
			startUTC, startLocal, tzid, err := parseDateTimeField(item.Start.DateTime, item.Start.TimeZone)
			if err != nil {
				return domain.Event{}, fmt.Errorf("google calendar: parse start: %w", err)
			}
			ev.StartUTC = startUTC
			ev.StartLocal = startLocal
			ev.TZID = tzid
		}
	}

	// Parse end time
	if item.End != nil {
		if item.End.Date != "" {
			t, err := time.Parse("2006-01-02", item.End.Date)
			if err != nil {
				return domain.Event{}, fmt.Errorf("google calendar: parse end date %q: %w", item.End.Date, err)
			}
			ev.EndUTC = t
			ev.EndLocal = item.End.Date
		} else if item.End.DateTime != "" {
			endUTC, endLocal, _, err := parseDateTimeField(item.End.DateTime, item.End.TimeZone)
			if err != nil {
				return domain.Event{}, fmt.Errorf("google calendar: parse end: %w", err)
			}
			ev.EndUTC = endUTC
			ev.EndLocal = endLocal
		}
	}

	// InstanceStartUTC for recurring instances
	if item.OriginalStartTime != nil {
		if item.OriginalStartTime.DateTime != "" {
			t, err := time.Parse(time.RFC3339, item.OriginalStartTime.DateTime)
			if err == nil {
				ev.InstanceStartUTC = t.UTC()
			}
		} else if item.OriginalStartTime.Date != "" {
			t, err := time.Parse("2006-01-02", item.OriginalStartTime.Date)
			if err == nil {
				ev.InstanceStartUTC = t
			}
		}
	}

	return ev, nil
}

// domainToGoogle converts a domain Event to a Google Calendar Event for write
// operations (create/update).
func domainToGoogle(e domain.Event) *calendar.Event {
	ge := &calendar.Event{
		Summary:     e.Summary,
		Description: e.Description,
		Location:    e.Location,
	}

	if e.AllDay {
		ge.Start = &calendar.EventDateTime{Date: e.StartUTC.Format("2006-01-02")}
		ge.End = &calendar.EventDateTime{Date: e.EndUTC.Format("2006-01-02")}
	} else {
		ge.Start = &calendar.EventDateTime{
			DateTime: e.StartUTC.UTC().Format(time.RFC3339),
			TimeZone: e.TZID,
		}
		ge.End = &calendar.EventDateTime{
			DateTime: e.EndUTC.UTC().Format(time.RFC3339),
			TimeZone: e.TZID,
		}
	}

	if e.Organizer != "" {
		ge.Organizer = &calendar.EventOrganizer{Email: e.Organizer}
	}
	for _, a := range e.Attendees {
		ge.Attendees = append(ge.Attendees, &calendar.EventAttendee{Email: a})
	}

	return ge
}

// patchToGoogle builds the body of an Events.Patch call: only the fields the
// patch sets. The client omits empty strings from JSON, so a field cleared
// with "" is named in ForceSendFields to reach the server as "" and clear it.
// allDay says whether the stored event is all-day; it matters only when the
// patch sets times.
func patchToGoogle(p domain.EventPatch, allDay bool) (*calendar.Event, error) {
	ge := &calendar.Event{}
	setText := func(field string, v *string, dst *string) {
		if v == nil {
			return
		}
		*dst = *v
		if *v == "" {
			ge.ForceSendFields = append(ge.ForceSendFields, field)
		}
	}
	setText("Summary", p.Summary, &ge.Summary)
	setText("Description", p.Description, &ge.Description)
	setText("Location", p.Location, &ge.Location)
	if allDay {
		if err := patchAllDayDates(ge, p.Start, p.End); err != nil {
			return nil, err
		}
		return ge, nil
	}
	// The instant keeps the offset the caller gave it. No timeZone is sent,
	// so the patch does not name one for the event.
	if p.Start != nil {
		ge.Start = &calendar.EventDateTime{DateTime: p.Start.Format(time.RFC3339)}
	}
	if p.End != nil {
		ge.End = &calendar.EventDateTime{DateTime: p.End.Format(time.RFC3339)}
	}
	return ge, nil
}

// patchAllDayDates keeps an all-day event all-day: it sends start.date and
// end.date and no dateTime, which Google would read as a switch to a timed
// event. Each date is the one on the value's own wall clock, not its UTC
// date, so 2026-11-26T00:00:00+01:00 stays the 26th; the iCloud driver
// writes the same dates. The end date is exclusive and must fall after the
// start date.
func patchAllDayDates(ge *calendar.Event, start, end *time.Time) error {
	if start != nil && end != nil && wallDate(*end) <= wallDate(*start) {
		return fmt.Errorf("all-day end date %s must be after start date %s",
			wallDate(*end), wallDate(*start))
	}
	if start != nil {
		ge.Start = &calendar.EventDateTime{Date: wallDate(*start)}
	}
	if end != nil {
		ge.End = &calendar.EventDateTime{Date: wallDate(*end)}
	}
	return nil
}

// wallDate is the calendar date of t in t's own location, as YYYY-MM-DD.
// The fixed-width form orders as a string in date order.
func wallDate(t time.Time) string {
	return t.Format(time.DateOnly)
}

// parseDateTimeField parses an RFC3339 dateTime string and an optional IANA
// timezone. Returns the UTC time, the local RFC3339 representation, and the
// TZID.
func parseDateTimeField(dateTime, tz string) (utc time.Time, local string, tzid string, err error) {
	t, err := time.Parse(time.RFC3339, dateTime)
	if err != nil {
		return time.Time{}, "", "", fmt.Errorf("parse %q: %w", dateTime, err)
	}

	utc = t.UTC()
	tzid = tz

	if tz != "" {
		loc, err := time.LoadLocation(tz)
		if err == nil {
			local = t.In(loc).Format(time.RFC3339)
			return utc, local, tzid, nil
		}
		// Fall through if location unknown — use the original string.
	}

	local = dateTime
	return utc, local, tzid, nil
}
