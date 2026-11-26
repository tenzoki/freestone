package google

import (
	"context"
	"strings"
	"testing"
	"time"

	domain "github.com/kai/freestone/internal/calendar"
)

// Google gives every occurrence its own id, so the iCloud scope inputs are
// refused rather than ignored, and no request reaches the API.
func TestScopeAndRecurrenceIDAreRefused(t *testing.T) {
	summary := "x"
	rid := time.Date(2026, 12, 3, 9, 0, 0, 0, time.UTC)
	for name, call := range map[string]func(*Driver) error{
		"update scope series": func(d *Driver) error {
			_, err := d.UpdateEvent(context.Background(), domain.EventPatch{CalendarID: "primary", UID: "evt-1", Summary: &summary, Scope: domain.ScopeSeries})
			return err
		},
		"update scope instance": func(d *Driver) error {
			_, err := d.UpdateEvent(context.Background(), domain.EventPatch{CalendarID: "primary", UID: "evt-1", Summary: &summary, Scope: domain.ScopeInstance})
			return err
		},
		"delete scope series": func(d *Driver) error {
			return d.DeleteEvent(context.Background(), domain.EventDeletion{CalendarID: "primary", UID: "evt-1", Scope: domain.ScopeSeries})
		},
		"delete scope instance": func(d *Driver) error {
			return d.DeleteEvent(context.Background(), domain.EventDeletion{CalendarID: "primary", UID: "evt-1", Scope: domain.ScopeInstance, RecurrenceID: rid})
		},
		"delete recurrence_id alone": func(d *Driver) error {
			return d.DeleteEvent(context.Background(), domain.EventDeletion{CalendarID: "primary", UID: "evt-1", RecurrenceID: rid})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newCalendarAPIHarness(t)
			err := call(h.driver())
			if err == nil || !strings.Contains(err.Error(), "addresses each occurrence of a series by its own uid") {
				t.Fatalf("err = %v, want scope refused for google", err)
			}
			if reqs := h.recorded(); len(reqs) != 0 {
				t.Errorf("requests = %+v, want none", reqs)
			}
		})
	}
}
