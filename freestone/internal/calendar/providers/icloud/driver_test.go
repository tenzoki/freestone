package icloud

import (
	"testing"
	"time"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

func makeCalendar(veventStr string) *ical.Calendar {
	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, "-//Test//Test//EN")

	vevent := ical.NewComponent(ical.CompEvent)
	// Parse simple property lines from the string.
	// This is a minimal test helper — not a full iCal parser.
	lines := splitLines(veventStr)
	for _, line := range lines {
		if line == "" {
			continue
		}
		key, rest := splitPropLine(line)
		if key == "" {
			continue
		}
		prop := ical.NewProp(key)
		// Check for params like TZID=...
		if idx := indexByte(key, ';'); idx >= 0 {
			// Re-parse with params
			realKey := key[:idx]
			paramStr := key[idx+1:]
			prop = ical.NewProp(realKey)
			pKey, pVal := splitPropLine(paramStr)
			if pKey != "" {
				prop.Params.Set(pKey, pVal)
			}
		}
		prop.Value = rest
		vevent.Props.Set(prop)
	}

	cal.Children = append(cal.Children, vevent)
	return cal
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitPropLine(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' || s[i] == '=' {
			return s[:i], s[i+1:]
		}
	}
	return "", ""
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func TestDecodeVEvent_Simple(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "test-uid-1")
	vevent.Props.SetText(ical.PropSummary, "Team Meeting")
	vevent.Props.SetText(ical.PropDescription, "Weekly standup")
	vevent.Props.SetText(ical.PropLocation, "Room 42")
	vevent.Props.SetText(ical.PropStatus, "CONFIRMED")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408T100000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260408T110000Z"
	vevent.Props.Set(endProp)

	e, err := decodeVEvent(vevent, "etag-123", "/cal/personal/")
	if err != nil {
		t.Fatal(err)
	}
	if e.UID != "test-uid-1" {
		t.Errorf("got UID %q", e.UID)
	}
	if e.Summary != "Team Meeting" {
		t.Errorf("got Summary %q", e.Summary)
	}
	if e.Location != "Room 42" {
		t.Errorf("got Location %q", e.Location)
	}
	if e.Provider != "icloud" {
		t.Errorf("got Provider %q", e.Provider)
	}
	if e.ETag != "etag-123" {
		t.Errorf("got ETag %q", e.ETag)
	}
	expected := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !e.StartUTC.Equal(expected) {
		t.Errorf("StartUTC = %v, want %v", e.StartUTC, expected)
	}
}

func TestDecodeVEvent_AllDay(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "allday-1")
	vevent.Props.SetText(ical.PropSummary, "Holiday")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408"
	startProp.Params.Set("VALUE", "DATE")
	vevent.Props.Set(startProp)

	e, err := decodeVEvent(vevent, "", "/cal/")
	if err != nil {
		t.Fatal(err)
	}
	if !e.AllDay {
		t.Error("expected AllDay=true")
	}
}

func TestDecodeVEvent_WithTZID(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "tz-1")
	vevent.Props.SetText(ical.PropSummary, "Berlin Meeting")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260408T120000"
	startProp.Params.Set("TZID", "Europe/Berlin")
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260408T130000"
	endProp.Params.Set("TZID", "Europe/Berlin")
	vevent.Props.Set(endProp)

	e, err := decodeVEvent(vevent, "", "/cal/")
	if err != nil {
		t.Fatal(err)
	}
	if e.TZID != "Europe/Berlin" {
		t.Errorf("got TZID %q", e.TZID)
	}
	// Berlin is UTC+2 in April
	expectedStart := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	if !e.StartUTC.Equal(expectedStart) {
		t.Errorf("StartUTC = %v, want %v", e.StartUTC, expectedStart)
	}
	if e.StartLocal == "" {
		t.Error("expected non-empty StartLocal")
	}
}

func TestExpandRRule_Weekly(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-1")
	vevent.Props.SetText(ical.PropSummary, "Weekly Sync")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=WEEKLY;COUNT=4"
	vevent.Props.Set(rruleProp)

	base := calendar.Event{
		UID:      "recur-1",
		Summary:  "Weekly Sync",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandRRule(vevent, base, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("expected 4 instances, got %d", len(events))
	}

	// Check the instances are 7 days apart.
	for i := 1; i < len(events); i++ {
		diff := events[i].StartUTC.Sub(events[i-1].StartUTC)
		if diff != 7*24*time.Hour {
			t.Errorf("instance %d: gap = %v, want 7 days", i, diff)
		}
	}

	// Each instance should have RecurringParentUID set.
	for _, e := range events {
		if e.RecurringParentUID != "recur-1" {
			t.Errorf("got RecurringParentUID %q", e.RecurringParentUID)
		}
	}
}

func TestExpandRRule_WithEXDATE(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-ex")
	vevent.Props.SetText(ical.PropSummary, "With Exclusion")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=WEEKLY;COUNT=4"
	vevent.Props.Set(rruleProp)

	// Exclude the second instance (April 8).
	exProp := ical.NewProp("EXDATE")
	exProp.Value = "20260408T090000Z"
	vevent.Props.Set(exProp)

	base := calendar.Event{
		UID:      "recur-ex",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandRRule(vevent, base, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 instances (1 excluded), got %d", len(events))
	}

	// Verify the excluded date is absent.
	excluded := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	for _, e := range events {
		if e.StartUTC.Equal(excluded) {
			t.Error("excluded date should not appear")
		}
	}
}

func TestExpandRRule_WithUNTIL(t *testing.T) {
	vevent := ical.NewComponent(ical.CompEvent)
	vevent.Props.SetText(ical.PropUID, "recur-until")

	startProp := ical.NewProp(ical.PropDateTimeStart)
	startProp.Value = "20260401T090000Z"
	vevent.Props.Set(startProp)

	endProp := ical.NewProp(ical.PropDateTimeEnd)
	endProp.Value = "20260401T100000Z"
	vevent.Props.Set(endProp)

	rruleProp := ical.NewProp("RRULE")
	rruleProp.Value = "FREQ=DAILY;UNTIL=20260405T090000Z"
	vevent.Props.Set(rruleProp)

	base := calendar.Event{
		UID:      "recur-until",
		StartUTC: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndUTC:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	events, err := expandRRule(vevent, base, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 instances (Apr 1-5), got %d", len(events))
	}
}
