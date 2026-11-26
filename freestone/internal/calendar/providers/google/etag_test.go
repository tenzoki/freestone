package google

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/calendar/v3"

	domain "github.com/kai/freestone/internal/calendar"
)

// googleConflict is Google's answer to a write whose If-Match is stale.
const googleConflict = `{"error":{"code":412,"message":"Precondition Failed","errors":[{"domain":"global","reason":"conditionNotMet","message":"Precondition Failed"}]}}`

// Google's JSON carries the entity tag quoted, as the header does; the
// domain holds it unquoted, the same form iCloud events carry.
func TestListEvents_ETagUnquoted(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodGet, "/calendars/primary/events", http.StatusOK, `{"items":[{
		"id":"evt-1","etag":"\"123\"","summary":"Standup","status":"confirmed",
		"start":{"dateTime":"2026-11-26T09:00:00Z"},"end":{"dateTime":"2026-11-26T10:00:00Z"}}]}`)

	list, err := h.driver().ListEvents(context.Background(), domain.EventQuery{CalendarID: "primary"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(list.Events) != 1 || list.Events[0].ETag != "123" {
		t.Fatalf("events = %+v, want one with ETag 123", list.Events)
	}
}

// An etag that is not a quoted string is not silently passed on: a value
// the driver cannot turn back into the header form would never match.
func TestEventFromGoogle_UnquotedETagIsError(t *testing.T) {
	_, err := eventFromGoogle(&calendar.Event{Id: "evt-1", Etag: "123"})
	if err == nil || !strings.Contains(err.Error(), `"123"`) {
		t.Fatalf("err = %v, want an error naming the etag", err)
	}
}

func TestDeleteEvent_ETagSentQuotedAsIfMatch(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodDelete, patchPath, http.StatusNoContent, "")

	if err := h.driver().DeleteEvent(context.Background(), "primary", "evt-1", "123"); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if got := h.only().Header.Get("If-Match"); got != `"123"` {
		t.Errorf("If-Match = %q, want %q", got, `"123"`)
	}
}

func TestDeleteEvent_NoETagSendsNoIfMatch(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodDelete, patchPath, http.StatusNoContent, "")

	if err := h.driver().DeleteEvent(context.Background(), "primary", "evt-1", ""); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if got, ok := h.only().Header["If-Match"]; ok {
		t.Errorf("If-Match = %q, want none", got)
	}
}

func TestDeleteEvent_StaleETagIsConflict(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodDelete, patchPath, http.StatusPreconditionFailed, googleConflict)

	err := h.driver().DeleteEvent(context.Background(), "primary", "evt-1", "stale")
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if !strings.Contains(err.Error(), "re-fetch") {
		t.Errorf("err = %q, want it to tell the caller to re-fetch", err)
	}
}

func TestUpdateEvent_StaleETagIsConflict(t *testing.T) {
	h := newCalendarAPIHarness(t)
	h.respond(http.MethodPatch, patchPath, http.StatusPreconditionFailed, googleConflict)

	summary := "Standup v2"
	_, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Summary: &summary, ETag: "stale",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	if got := h.only().Header.Get("If-Match"); got != `"stale"` {
		t.Errorf("If-Match = %q, want %q", got, `"stale"`)
	}
}

// A time-changing patch reads the stored event first to learn how it is
// dated. That read must not replace the caller's etag: the PATCH stays
// conditional on what the caller saw, not on what the read returned.
func TestUpdateEvent_TimeChangeKeepsCallerETagOnPatch(t *testing.T) {
	h := newCalendarAPIHarness(t)
	stored := *patchedEvent
	stored.Etag = `"newer"`
	h.respond(http.MethodGet, patchPath, http.StatusOK, &stored)
	h.respond(http.MethodPatch, patchPath, http.StatusPreconditionFailed, googleConflict)

	start := time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	_, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Start: &start, End: &end, ETag: "seen",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
	reqs := h.recorded()
	if len(reqs) != 2 || reqs[0].Method != http.MethodGet || reqs[1].Method != http.MethodPatch {
		t.Fatalf("requests = %+v, want GET then PATCH", reqs)
	}
	if got := reqs[1].Header.Get("If-Match"); got != `"seen"` {
		t.Errorf("PATCH If-Match = %q, want the caller's %q", got, `"seen"`)
	}
}

// A current etag goes through, and the event that comes back carries
// Google's new etag unquoted, ready for the next conditional write.
func TestUpdateEvent_CurrentETagSucceeds(t *testing.T) {
	h := newCalendarAPIHarness(t)
	answer := *patchedEvent
	answer.Etag = `"124"`
	h.respond(http.MethodPatch, patchPath, http.StatusOK, &answer)

	summary := "Standup v2"
	updated, err := h.driver().UpdateEvent(context.Background(), domain.EventPatch{
		CalendarID: "primary", UID: "evt-1", Summary: &summary, ETag: "123",
	})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if got := h.only().Header.Get("If-Match"); got != `"123"` {
		t.Errorf("If-Match = %q, want %q", got, `"123"`)
	}
	if updated.ETag != "124" {
		t.Errorf("returned ETag = %q, want 124", updated.ETag)
	}
}
