package main

import (
	gcal "google.golang.org/api/calendar/v3"
	gmailapi "google.golang.org/api/gmail/v1"

	"github.com/kai/freestone/internal/config"
)

// googleScopes is the single source of truth for the OAuth scopes freestone
// requests. Both `auth google` (consent) and `serve` (token use) call it, so
// the scopes a token is granted and the scopes it is used under cannot drift.
//
// Each scope is the narrowest one that covers every API call the drivers make:
//
//   - calendar.calendarlist.readonly: CalendarList.List (ListCalendars).
//   - calendar.events: Events.List/Get/Insert/Update/Delete. Not
//     calendar.events.owned, which would hide events on shared calendars.
//   - gmail.modify: Labels.List/Get, Messages.List/Get, Messages.Modify
//     (mark read; only gmail.modify or full mail access allow it), and
//     Drafts.Create. Gmail has no draft-only scope, and gmail.modify also
//     permits Messages.Send and Drafts.Send. The mail write-surface guarantee
//     test (internal/mail/guarantee_test.go) is what keeps the code from
//     ever calling them.
func googleScopes(cfg *config.Config) []string {
	var scopes []string
	if cfg.Calendar.Google != nil {
		scopes = append(scopes, gcal.CalendarCalendarlistReadonlyScope, gcal.CalendarEventsScope)
	}
	if cfg.Mail.Google != nil {
		scopes = append(scopes, gmailapi.GmailModifyScope)
	}
	return scopes
}
