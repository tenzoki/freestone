package calendar

import (
	"context"
	"testing"
	"time"
)

func TestNewRegistry_DuplicateName(t *testing.T) {
	f1 := &Fake{ProviderName: "test"}
	f2 := &Fake{ProviderName: "test"}
	_, err := NewRegistry(f1, f2)
	if err == nil {
		t.Fatal("expected error for duplicate provider name")
	}
}

func TestRegistry_ProviderLookup(t *testing.T) {
	f := &Fake{ProviderName: "icloud"}
	r := NewFakeRegistry(f)

	p, err := r.Provider("icloud")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "icloud" {
		t.Errorf("expected icloud, got %s", p.Name())
	}

	_, err = r.Provider("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRegistry_ListAllCalendars(t *testing.T) {
	f1 := &Fake{
		ProviderName: "icloud",
		Calendars: []Calendar{
			{ID: "cal1", Name: "Personal"},
		},
	}
	f2 := &Fake{
		ProviderName: "google",
		Calendars: []Calendar{
			{ID: "cal2", Name: "Work"},
		},
	}
	r := NewFakeRegistry(f1, f2)

	cals, err := r.ListAllCalendars(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cals) != 2 {
		t.Fatalf("expected 2 calendars, got %d", len(cals))
	}

	// Verify provider field is set.
	providers := map[string]bool{}
	for _, c := range cals {
		providers[c.Provider] = true
	}
	if !providers["icloud"] || !providers["google"] {
		t.Errorf("expected both providers, got %v", providers)
	}
}

func TestFake_ListEventsWithTimeFilter(t *testing.T) {
	now := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	f := &Fake{
		ProviderName: "icloud",
		Events: map[string][]Event{
			"cal1": {
				{UID: "e1", Summary: "Past", StartUTC: now.Add(-2 * time.Hour)},
				{UID: "e2", Summary: "Now", StartUTC: now},
				{UID: "e3", Summary: "Future", StartUTC: now.Add(2 * time.Hour)},
			},
		},
	}

	events, err := f.ListEvents(context.Background(), EventQuery{
		CalendarID: "cal1",
		Start:      now.Add(-1 * time.Hour),
		End:        now.Add(1 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].UID != "e2" {
		t.Errorf("expected e2, got %s", events[0].UID)
	}
}

func TestFake_GetEvent(t *testing.T) {
	f := &Fake{
		ProviderName: "icloud",
		Events: map[string][]Event{
			"cal1": {
				{UID: "e1", Summary: "Test Event"},
			},
		},
	}

	e, err := f.GetEvent(context.Background(), "cal1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if e.Summary != "Test Event" {
		t.Errorf("got summary %q", e.Summary)
	}
	if e.Provider != "icloud" {
		t.Errorf("expected provider icloud, got %s", e.Provider)
	}

	_, err = f.GetEvent(context.Background(), "cal1", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing event")
	}
}
