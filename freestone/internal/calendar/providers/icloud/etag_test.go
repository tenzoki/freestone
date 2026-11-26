package icloud

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/kai/freestone/internal/calendar"
)

// A caller etag that is not the stored one is a conflict found before any
// write: no PUT goes out and the stored object is untouched.
func TestUpdateEvent_StaleETagIsConflictWithoutPut(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()

	summary := "Standup v2"
	_, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Summary: &summary, ETag: "stale", Scope: calendar.ScopeSeries,
	})
	if !errors.Is(err, calendar.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "re-fetch") {
		t.Errorf("err = %q, want it to tell the caller to re-fetch", err)
	}
	if puts := h.recordedWith(http.MethodPut); len(puts) != 0 {
		t.Errorf("PUT requests = %+v, want none", puts)
	}
	if o, _ := h.object(href); o.ETag != etag {
		t.Errorf("stored ETag = %q, want %q unchanged", o.ETag, etag)
	}
}

func TestUpdateEvent_CurrentETagWritesConditionally(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()

	summary := "Standup v2"
	updated, err := d.UpdateEvent(context.Background(), calendar.EventPatch{
		CalendarID: cal, UID: "uid-1", Summary: &summary, ETag: etag, Scope: calendar.ScopeSeries,
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	puts := h.recordedWith(http.MethodPut)
	if len(puts) != 1 || puts[0].IfMatch != strconv.Quote(etag) {
		t.Errorf("PUT requests = %+v, want one with If-Match %s", puts, strconv.Quote(etag))
	}
	if o, _ := h.object(href); updated.ETag != o.ETag || o.ETag == etag {
		t.Errorf("returned ETag %q, stored %q, want the new stored one", updated.ETag, o.ETag)
	}
}

// A stale etag on delete is refused by the server with 412, which the
// driver's HTTP client decorator turns into ErrConflict; nothing is deleted.
func TestDeleteEvent_StaleETagIsConflict(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	h.seedObject(href, richEventICS)
	d := h.driver()

	err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", ETag: "stale", Scope: calendar.ScopeSeries})
	if !errors.Is(err, calendar.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	dels := h.recordedWith(http.MethodDelete)
	if len(dels) != 1 || dels[0].IfMatch != strconv.Quote("stale") {
		t.Errorf("DELETE requests = %+v, want one with If-Match %q", dels, strconv.Quote("stale"))
	}
	if _, ok := h.object(href); !ok {
		t.Errorf("object deleted despite the stale etag")
	}
}

func TestDeleteEvent_CurrentETagSentQuoted(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "ABC-random.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()

	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", ETag: etag, Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	dels := h.recordedWith(http.MethodDelete)
	if len(dels) != 1 || dels[0].Path != href || dels[0].IfMatch != strconv.Quote(etag) {
		t.Errorf("DELETE requests = %+v, want one on %s with If-Match %s", dels, href, strconv.Quote(etag))
	}
	if _, ok := h.object(href); ok {
		t.Errorf("object still stored after delete")
	}
}

// Without a caller etag the delete is still conditional, on the version
// the driver just resolved, so a change made in between is not lost.
func TestDeleteEvent_NoETagUsesResolvedOne(t *testing.T) {
	h := newCalDAVHarness(t)
	cal := h.addCalendar("personal", "Personal")
	href := cal + "uid-1.ics"
	etag := h.seedObject(href, richEventICS)
	d := h.driver()

	if err := d.DeleteEvent(context.Background(), calendar.EventDeletion{CalendarID: cal, UID: "uid-1", Scope: calendar.ScopeSeries}); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	dels := h.recordedWith(http.MethodDelete)
	if len(dels) != 1 || dels[0].IfMatch != strconv.Quote(etag) {
		t.Errorf("DELETE requests = %+v, want one with If-Match %s", dels, strconv.Quote(etag))
	}
}
