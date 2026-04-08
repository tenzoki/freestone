package calendar

import (
	"context"
	"fmt"
)

// Fake is an in-memory calendar provider for testing.
type Fake struct {
	ProviderName string
	Calendars    []Calendar
	Events       map[string][]Event // keyed by calendar ID
}

func (f *Fake) Name() string { return f.ProviderName }

func (f *Fake) ListCalendars(_ context.Context) ([]Calendar, error) {
	return f.Calendars, nil
}

// ListEvents returns events with StartUTC in the half-open interval
// [q.Start, q.End), matching standard CalDAV time-range semantics.
func (f *Fake) ListEvents(_ context.Context, q EventQuery) ([]Event, error) {
	events := f.Events[q.CalendarID]
	var result []Event
	for _, e := range events {
		if !q.Start.IsZero() && e.StartUTC.Before(q.Start) {
			continue
		}
		if !q.End.IsZero() && !e.StartUTC.Before(q.End) {
			continue
		}
		e.Provider = f.ProviderName
		result = append(result, e)
	}
	return result, nil
}

func (f *Fake) GetEvent(_ context.Context, calendarID, uid string) (*Event, error) {
	for _, e := range f.Events[calendarID] {
		if e.UID == uid {
			e.Provider = f.ProviderName
			return &e, nil
		}
	}
	return nil, fmt.Errorf("fake: event %s not found in %s", uid, calendarID)
}

func (f *Fake) CreateEvent(_ context.Context, _ string, _ Event) (*Event, error) {
	return nil, fmt.Errorf("fake: create not implemented in phase 1")
}

func (f *Fake) UpdateEvent(_ context.Context, _ Event) (*Event, error) {
	return nil, fmt.Errorf("fake: update not implemented in phase 1")
}

func (f *Fake) DeleteEvent(_ context.Context, _, _, _ string) error {
	return fmt.Errorf("fake: delete not implemented in phase 1")
}

// NewFakeRegistry creates a Registry populated with the given fakes.
func NewFakeRegistry(fakes ...*Fake) *Registry {
	providers := make([]Provider, len(fakes))
	for i, f := range fakes {
		providers[i] = f
	}
	r, err := NewRegistry(providers...)
	if err != nil {
		panic(err)
	}
	return r
}
