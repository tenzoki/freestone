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

	// Organizer
	if item.Organizer != nil {
		if item.Organizer.DisplayName != "" {
			ev.Organizer = item.Organizer.DisplayName
		} else {
			ev.Organizer = item.Organizer.Email
		}
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
