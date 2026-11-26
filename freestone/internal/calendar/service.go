// Package calendar defines the domain types and provider interface for
// calendar operations. Provider drivers live in subdirectories.
package calendar

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Event represents a single calendar event (possibly an expanded instance
// of a recurring event).
type Event struct {
	Provider           string    `json:"provider"`
	CalendarID         string    `json:"calendar_id"`
	UID                string    `json:"uid"`
	Summary            string    `json:"summary"`
	Description        string    `json:"description,omitempty"`
	Location           string    `json:"location,omitempty"`
	StartUTC           time.Time `json:"start_utc"`
	EndUTC             time.Time `json:"end_utc"`
	StartLocal         string    `json:"start_local,omitempty"`
	EndLocal           string    `json:"end_local,omitempty"`
	TZID               string    `json:"tzid,omitempty"`
	AllDay             bool      `json:"all_day"`
	Organizer          string    `json:"organizer,omitempty"`
	Attendees          []string  `json:"attendees,omitempty"`
	Status             string    `json:"status,omitempty"`
	ETag               string    `json:"etag,omitempty"`
	RecurringParentUID string    `json:"recurring_parent_uid,omitempty"`
	InstanceStartUTC   time.Time `json:"instance_start_utc,omitempty"`
}

// Calendar represents a single calendar container.
type Calendar struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	TimeZone    string `json:"timezone,omitempty"`
}

// EventQuery specifies filters for listing events.
type EventQuery struct {
	Provider   string
	CalendarID string
	Start      time.Time
	End        time.Time
}

// EventPatch names the fields of an existing event to change. A nil field
// keeps the stored value; a pointer to "" clears a text field. Start and End
// are given together or not at all. Providers apply the patch to the stored
// event, so every property the domain does not model (recurrence, alarms,
// attendee parameters, vendor extensions) survives an update.
type EventPatch struct {
	CalendarID  string
	UID         string
	Summary     *string
	Description *string
	Location    *string
	Start       *time.Time
	End         *time.Time
	// ETag is the opaque entity tag, unquoted; "" means the caller asks for
	// no conditional write of its own.
	ETag string
}

// Provider defines the contract each calendar backend must implement.
type Provider interface {
	Name() string
	ListCalendars(ctx context.Context) ([]Calendar, error)
	ListEvents(ctx context.Context, q EventQuery) ([]Event, error)
	GetEvent(ctx context.Context, calendarID, uid string) (*Event, error)
	// Phase 2 write operations
	CreateEvent(ctx context.Context, calendarID string, e Event) (*Event, error)
	UpdateEvent(ctx context.Context, p EventPatch) (*Event, error)
	DeleteEvent(ctx context.Context, calendarID, uid, etag string) error
}

// Registry routes calendar operations to the appropriate provider.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry creates a Registry from one or more providers.
// Returns an error if provider names collide.
func NewRegistry(ps ...Provider) (*Registry, error) {
	m := make(map[string]Provider, len(ps))
	for _, p := range ps {
		name := p.Name()
		if _, exists := m[name]; exists {
			return nil, fmt.Errorf("calendar: duplicate provider name %q", name)
		}
		m[name] = p
	}
	return &Registry{providers: m}, nil
}

// Provider returns the named provider or an error.
func (r *Registry) Provider(name string) (Provider, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("calendar: unknown provider %q", name)
	}
	return p, nil
}

// ListAllCalendars aggregates calendars from every registered provider.
// If some providers fail, partial results from successful providers are
// still returned. Errors are collected and returned as a single joined error.
func (r *Registry) ListAllCalendars(ctx context.Context) ([]Calendar, error) {
	var all []Calendar
	var errs []string
	for _, name := range r.ProviderNames() {
		p := r.providers[name]
		cals, err := p.ListCalendars(ctx)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for i := range cals {
			cals[i].Provider = name
		}
		all = append(all, cals...)
	}
	if len(errs) > 0 && len(all) == 0 {
		return nil, fmt.Errorf("calendar: all providers failed: %s", strings.Join(errs, "; "))
	}
	// Partial success: return results even if some providers failed.
	return all, nil
}

// ProviderNames returns the names of all registered providers in sorted order.
func (r *Registry) ProviderNames() []string {
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
