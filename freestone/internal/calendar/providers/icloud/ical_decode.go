package icloud

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// decodeEvents converts an ical.Calendar into domain Events.
// If the VEVENT has an RRULE, it expands instances via rrule-go within
// the [windowStart, windowEnd) range.
func decodeEvents(cal *ical.Calendar, etag, calendarID string, windowStart, windowEnd time.Time, log *slog.Logger) ([]calendar.Event, error) {
	var events []calendar.Event

	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}

		base, err := decodeVEvent(child, etag, calendarID)
		if err != nil {
			uid := "unknown"
			if p := child.Props.Get(ical.PropUID); p != nil {
				uid = p.Value
			}
			log.Warn("skipping malformed VEVENT", "uid", uid, "err", err)
			continue
		}

		rruleProp := child.Props.Get("RRULE")
		if rruleProp != nil {
			expanded, err := expandRRule(child, base, windowStart, windowEnd)
			if err == nil && len(expanded) > 0 {
				events = append(events, expanded...)
				continue
			}
			// Fall through to single event if expansion fails.
		}

		events = append(events, base)
	}
	return events, nil
}

func decodeVEvent(vevent *ical.Component, etag, calendarID string) (calendar.Event, error) {
	e := calendar.Event{
		Provider:   "icloud",
		CalendarID: calendarID,
		ETag:       etag,
	}

	if p := vevent.Props.Get(ical.PropUID); p != nil {
		e.UID = p.Value
	}
	if p := vevent.Props.Get(ical.PropSummary); p != nil {
		e.Summary = p.Value
	}
	if p := vevent.Props.Get(ical.PropDescription); p != nil {
		e.Description = p.Value
	}
	if p := vevent.Props.Get(ical.PropLocation); p != nil {
		e.Location = p.Value
	}
	if p := vevent.Props.Get(ical.PropStatus); p != nil {
		e.Status = p.Value
	}
	if p := vevent.Props.Get(ical.PropOrganizer); p != nil {
		e.Organizer = p.Value
	}

	for _, p := range vevent.Props.Values(ical.PropAttendee) {
		e.Attendees = append(e.Attendees, p.Value)
	}

	// Parse DTSTART
	if p := vevent.Props.Get(ical.PropDateTimeStart); p != nil {
		start, tzid, allDay, err := parseDateTime(p)
		if err != nil {
			return e, fmt.Errorf("parse DTSTART %q: %w", p.Value, err)
		}
		e.StartUTC = start.UTC()
		e.TZID = tzid
		e.AllDay = allDay
		if tzid != "" {
			e.StartLocal = start.Format(time.RFC3339)
		}
	}

	// Parse DTEND
	if p := vevent.Props.Get(ical.PropDateTimeEnd); p != nil {
		end, _, _, err := parseDateTime(p)
		if err != nil {
			return e, fmt.Errorf("parse DTEND %q: %w", p.Value, err)
		}
		e.EndUTC = end.UTC()
		if e.TZID != "" {
			e.EndLocal = end.Format(time.RFC3339)
		}
	}

	return e, nil
}

func parseDateTime(p *ical.Prop) (t time.Time, tzid string, allDay bool, err error) {
	// Check for TZID parameter.
	tzid = p.Params.Get("TZID")

	// Check VALUE=DATE for all-day events.
	if p.Params.Get("VALUE") == "DATE" {
		allDay = true
		t, err = time.Parse("20060102", p.Value)
		return t, tzid, allDay, err
	}

	// Try parsing with explicit timezone. An unresolvable TZID still falls
	// through to the UTC/floating parse below; that fallback is intentional
	// until unknown zones are reported as decode errors.
	if tzid != "" {
		loc, locErr := resolveLocation(tzid)
		if locErr == nil {
			t, err = time.ParseInLocation("20060102T150405", p.Value, loc)
			return t, tzid, false, err
		}
	}

	// UTC timestamp (ends with Z).
	if len(p.Value) > 0 && p.Value[len(p.Value)-1] == 'Z' {
		t, err = time.Parse("20060102T150405Z", p.Value)
		return t, "", false, err
	}

	// Floating time — treat as UTC.
	t, err = time.Parse("20060102T150405", p.Value)
	return t, "", false, err
}
