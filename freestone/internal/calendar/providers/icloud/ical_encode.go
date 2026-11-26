package icloud

import (
	"fmt"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"

	"github.com/kai/freestone/internal/calendar"
)

// encodeEvent builds a VCALENDAR containing a single VEVENT from a domain Event.
// If UID is empty, a new UUID is generated. DTSTAMP is always set to now.
func encodeEvent(e calendar.Event) (*ical.Calendar, string, error) {
	uid := e.UID
	if uid == "" {
		uid = uuid.New().String()
	}

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//freestone//EN")

	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, uid)
	vevent.Props.SetText(ical.PropDateTimeStamp, time.Now().UTC().Format("20060102T150405Z"))

	// An all-day event is a DATE pair with an exclusive DTEND (RFC 5545
	// §3.6.1); the domain carries the date as UTC midnight. Timed events
	// are written as UTC DATE-TIMEs. Neither goes through SetText, which
	// would label the value VALUE=TEXT.
	setTime := func(name string, t time.Time) {
		if e.AllDay {
			vevent.Props.SetDate(name, t.UTC())
		} else {
			vevent.Props.SetDateTime(name, t.UTC())
		}
	}
	if !e.StartUTC.IsZero() {
		setTime(ical.PropDateTimeStart, e.StartUTC)
	}
	if !e.EndUTC.IsZero() {
		setTime(ical.PropDateTimeEnd, e.EndUTC)
	}

	if e.Summary != "" {
		vevent.Props.SetText(ical.PropSummary, e.Summary)
	}
	if e.Description != "" {
		vevent.Props.SetText(ical.PropDescription, e.Description)
	}
	if e.Location != "" {
		vevent.Props.SetText(ical.PropLocation, e.Location)
	}
	if e.Organizer != "" {
		vevent.Props.SetText(ical.PropOrganizer, e.Organizer)
	}
	for _, a := range e.Attendees {
		vevent.Props.Add(&ical.Prop{
			Name:  ical.PropAttendee,
			Value: a,
		})
	}

	cal.Children = append(cal.Children, vevent)
	return cal, uid, nil
}

// eventObjectPath returns the CalDAV path for an event within a calendar.
func eventObjectPath(calendarID, uid string) string {
	return fmt.Sprintf("%s%s.ics", calendarID, uid)
}
