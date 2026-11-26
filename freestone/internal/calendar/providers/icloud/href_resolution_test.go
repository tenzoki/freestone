package icloud

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/emersion/go-ical"

	"github.com/kai/freestone/internal/calendar"
)

// D8: other CalDAV clients choose their own object hrefs (RFC 4791 §5.3.2),
// so an event's object is found by its UID, never derived from it.

// otherClientHref is where another client stored uid-1: not <uid>.ics.
const otherClientHref = "/user/calendars/personal/ABC-random.ics"

// uid10ICS is an object whose UID contains "uid-1" as a substring, which a
// server-side text-match on uid-1 also matches.
const uid10ICS = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//harness//EN
BEGIN:VEVENT
UID:uid-10
DTSTAMP:20261101T080000Z
DTSTART:20261127T090000Z
DTEND:20261127T100000Z
SUMMARY:Other
END:VEVENT
END:VCALENDAR
`

// objectsWithUID returns the hrefs of every stored object holding a VEVENT
// with the given UID.
func (h *calDAVHarness) objectsWithUID(uid string) []string {
	var out []string
	for _, href := range h.hrefs() {
		o, _ := h.object(href)
		for _, c := range o.Data.Children {
			if c.Name != ical.CompEvent {
				continue
			}
			if p := c.Props.Get(ical.PropUID); p != nil && p.Value == uid {
				out = append(out, href)
				break
			}
		}
	}
	return out
}

func TestGetEvent_ResolvesHrefByUID(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	etag := h.seedObject(otherClientHref, richEventICS)
	h.seedObject(cal+"XYZ-other.ics", uid10ICS)
	d := h.driver()

	got, err := d.GetEvent(context.Background(), cal, "uid-1")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.UID != "uid-1" || got.Summary != "Standup" || got.ETag != etag {
		t.Errorf("GetEvent = uid %q summary %q etag %q, want uid-1 Standup %q", got.UID, got.Summary, got.ETag, etag)
	}
}

// TestUpdateEvent_WritesBackToResolvedHref also runs the step 3 survival
// assertions through the REPORT path: VTIMEZONE and VALARM are still there.
func TestUpdateEvent_WritesBackToResolvedHref(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	etag := h.seedObject(otherClientHref, richEventICS)
	h.seedObject(cal+"XYZ-other.ics", uid10ICS)
	d := h.driver()

	if _, err := updateSummary(context.Background(), d, cal, "uid-1", "Standup v2"); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	if got := h.objectsWithUID("uid-1"); len(got) != 1 || got[0] != otherClientHref {
		t.Fatalf("objects with uid-1 = %v, want only %s", got, otherClientHref)
	}
	stored, _ := h.object(otherClientHref)
	root := stored.Data.Component
	harnessComponent(t, root, ical.CompTimezone, 0)
	master := harnessComponent(t, root, ical.CompEvent, 0)
	if master == nil {
		t.FailNow()
	}
	if got := propValue(t, master, ical.PropSummary); got != "Standup v2" {
		t.Errorf("SUMMARY = %q, want Standup v2", got)
	}
	harnessComponent(t, master, ical.CompAlarm, 0)

	puts := h.recordedWith(http.MethodPut)
	if len(puts) != 1 || puts[0].Path != otherClientHref {
		t.Fatalf("PUT requests = %+v, want one on %s", puts, otherClientHref)
	}
	if want := strconv.Quote(etag); puts[0].IfMatch != want {
		t.Errorf("PUT If-Match = %q, want %q", puts[0].IfMatch, want)
	}
	if other, _ := h.object(cal + "XYZ-other.ics"); propValue(t, other.Data.Children[0], ical.PropSummary) != "Other" {
		t.Errorf("uid-10 object was modified")
	}
}

func TestDeleteEvent_RemovesResolvedHref(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(otherClientHref, richEventICS)
	h.seedObject(cal+"XYZ-other.ics", uid10ICS)
	d := h.driver()

	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if got := h.hrefs(); len(got) != 1 || got[0] != cal+"XYZ-other.ics" {
		t.Errorf("stored objects = %v, want only the uid-10 object", got)
	}
	dels := h.recordedWith(http.MethodDelete)
	if len(dels) != 1 || dels[0].Path != otherClientHref {
		t.Errorf("DELETE requests = %+v, want one on %s", dels, otherClientHref)
	}
}

// TestGetEvent_SubstringUIDIsNotAMatch: the server's text-match is a
// substring test, so uid-10 answers a query for uid-1; it must not be
// taken for it.
func TestGetEvent_SubstringUIDIsNotAMatch(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(cal+"uid-10.ics", uid10ICS)
	d := h.driver()

	e, err := d.GetEvent(context.Background(), cal, "uid-1")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("GetEvent = %+v, %v; want a not-found error", e, err)
	}
}

func TestLoadObject_DuplicateUIDNamesEveryHref(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(otherClientHref, richEventICS)
	h.seedObject(cal+"uid-1.ics", richEventICS)
	d := h.driver()

	e, err := d.GetEvent(context.Background(), cal, "uid-1")
	if err == nil {
		t.Fatalf("GetEvent = %+v; want an error for two objects with uid-1", e)
	}
	for _, href := range []string{otherClientHref, cal + "uid-1.ics"} {
		if !strings.Contains(err.Error(), href) {
			t.Errorf("error %q does not name %s", err, href)
		}
	}
	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err == nil {
		t.Errorf("DeleteEvent: want the duplicate error, got nil")
	}
	if got := h.hrefs(); len(got) != 2 {
		t.Errorf("stored objects = %v, want both kept", got)
	}
}

// TestLoadObject_ICloudPartialRetrieval keeps loadObject off the request
// shape iCloud strips: against a server that drops VEVENT properties from a
// partial-retrieval answer, the resolved object is still whole.
func TestLoadObject_ICloudPartialRetrieval(t *testing.T) {
	h := newCalDAVHarness(t).withICloudPartialRetrieval()
	cal := h.addCalendar("personal", "Personal")
	h.seedObject(otherClientHref, richEventICS)
	d := h.driver()

	obj, err := d.loadObject(context.Background(), cal, "uid-1")
	if err != nil {
		t.Fatalf("loadObject: %v", err)
	}
	if obj.Path != otherClientHref {
		t.Errorf("Path = %s, want %s", obj.Path, otherClientHref)
	}
	root := obj.Data.Component
	harnessComponent(t, root, ical.CompTimezone, 0)
	master := harnessComponent(t, root, ical.CompEvent, 0)
	if master == nil {
		t.FailNow()
	}
	for name, want := range map[string]string{
		ical.PropUID:           "uid-1",
		ical.PropSummary:       "Standup",
		ical.PropDateTimeStart: "20261126T090000",
	} {
		if got := propValue(t, master, name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	harnessComponent(t, master, ical.CompAlarm, 0)
	if got := h.recordedWith("REPORT"); len(got) != 1 || got[0].Path != cal {
		t.Errorf("REPORT requests = %+v, want one on %s", got, cal)
	}
}
