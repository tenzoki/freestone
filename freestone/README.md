# freestone

A personal MCP server that extends Claude Code with calendar, mail, and file access tools. Runs locally on macOS, speaks MCP over stdio.

## Status

**Phase 2** — Read/write with iCloud and Google providers. The audit logger exists but is not yet wired into `serve` (see Audit Log).

## Install

```bash
cd freestone
make install
```

This builds the binary and copies it to `~/.local/bin/freestone-mcp`.

## Setup

### 1. Create config directory

```bash
mkdir -p ~/.config/freestone
cp example.config.toml ~/.config/freestone/config.toml
cp example.access.rules ~/.config/freestone/access.rules
```

Edit `config.toml` and uncomment the providers you want. Edit `access.rules` to allow the directories Claude should access.

### 2. iCloud setup

iCloud Calendar and Mail require an app-specific password:

1. Go to [Apple ID](https://appleid.apple.com) > Sign-In and Security > App-Specific Passwords
2. Generate a new password (name it "freestone" or similar)
3. Store it in macOS Keychain:

```bash
# For calendar
security add-generic-password -s "freestone-icloud-calendar" -a "your@apple-id.com" -w "xxxx-xxxx-xxxx-xxxx"

# For mail (can be the same password or a separate one)
security add-generic-password -s "freestone-icloud-mail" -a "your@apple-id.com" -w "xxxx-xxxx-xxxx-xxxx"
```

The `-s` value must match `secret_ref` in your `config.toml`.

### 3. Google setup

Google Calendar and Gmail use OAuth2. You need a Google Cloud project with the Calendar and Gmail APIs enabled.

#### Create OAuth credentials

1. Go to [Google Cloud Console](https://console.cloud.google.com/) > APIs & Services > Credentials
2. Create an **OAuth 2.0 Client ID** of type **Desktop app**
3. Note the Client ID and Client Secret
4. Under **Authorized redirect URIs**, add `http://127.0.0.1` (the auth flow uses a random port on loopback)
5. Enable the **Google Calendar API** and **Gmail API** for your project

#### Store credentials in Keychain

```bash
security add-generic-password -s "freestone-google-client-id" -a "freestone" -w "YOUR_CLIENT_ID.apps.googleusercontent.com"
security add-generic-password -s "freestone-google-client-secret" -a "freestone" -w "YOUR_CLIENT_SECRET"
```

The `-s` values must match `client_id_ref` and `client_secret_ref` in your `config.toml`.

#### Authorize

Run the auth flow to get a refresh token:

```bash
freestone-mcp auth google
```

This opens your browser for Google consent. freestone requests the narrowest scopes that cover its tools, and only for the Google providers enabled in your config:
- `https://www.googleapis.com/auth/calendar.calendarlist.readonly` (list calendars)
- `https://www.googleapis.com/auth/calendar.events` (read + create/update/delete events)
- `https://www.googleapis.com/auth/gmail.modify` (read + draft save + mark read)

Gmail has no draft-only scope: `gmail.modify` also permits sending and trashing mail. freestone never does either; `internal/mail/guarantee_test.go` fails the build if the mail code ever references a send, delete, trash, move or expunge operation.

**Upgrading from a read-only token.** Tokens authorized before the write tools existed carry only `calendar.readonly` / `gmail.readonly`, and every Google write tool fails with `403 insufficient authentication scopes`. The stored token records no scopes, so freestone cannot detect this at startup. Run `freestone-mcp auth google` again to re-consent.

The refresh token is stored in Keychain under the `token_ref` from your config (default: `freestone-google-token`).

To revoke access and delete the stored token:

```bash
freestone-mcp auth google --revoke
```

### 4. Configure Claude Code

Register freestone as a user-scoped MCP server (adjust the path if you installed elsewhere):

```bash
claude mcp add --scope user freestone -- ~/.local/bin/freestone-mcp serve
```

`freestone-mcp serve` reads `~/.config/freestone/config.toml` (override with `--config`) and exits at once if the file is missing, invalid or enables no provider, so create and edit it first (step 1).

## Tools

The `provider` parameter selects which backend to use (`icloud` or `google`). Omit it on list operations to aggregate results from all enabled providers. Every tool call is bounded by a 30-second timeout; this covers iCloud mail too, where a timed-out or cancelled call closes the IMAP connection and returns an error.

### Calendar

| Tool | Description |
|------|-------------|
| `calendar_list_calendars` | List calendars. Omit `provider` to list from all enabled providers. |
| `calendar_list_events` | List events in a calendar within a time range (RFC3339 `start`/`end`). Returns `{"events": [...], "warnings": [...]}`: `events` is `[]` for an empty window, `warnings` names each calendar object that could not be read and was left out (its href and the reason), and is omitted when empty. |
| `calendar_get_event` | Get a single event by UID. |
| `calendar_create_event` | Create a new event (summary, start, end, optional description/location, `all_day`). |
| `calendar_update_event` | Partial update of an existing event by UID: only the fields you pass change. A recurring iCloud event needs `scope: "series"` (see **Recurring events**). |
| `calendar_delete_event` | Delete an event by UID, optionally conditional on its `etag`. A recurring iCloud event needs `scope`: `"series"`, or `"instance"` with `recurrence_id` (see **Recurring events**). |

**Events.** An event carries `provider`, `calendar_id` (on both providers), `uid`, `summary`, `description`, `location`, `start_utc`/`end_utc`, `start_local`/`end_local` and `tzid` (the time zone as the producer named it), `all_day`, `organizer`, `attendees`, `status`, `etag`, and for an instance of a recurring series `recurring_parent_uid` and `instance_start_utc`, and on iCloud `recurrence_id`: the occurrence's original start (RFC3339), which is its `RECURRENCE-ID` when it was moved, not its new start. `organizer` and `attendees` are mail addresses on both providers: Google gives the organizer's address, not the display name, and iCloud strips the `mailto:` prefix (other URI schemes are kept as they are).

**Partial updates (`calendar_update_event`).** For `summary`, `description` and `location`, omitting the field or passing `null` keeps the current value, and `""` clears it. `start` and `end` are given together or not at all, and `end` must be after `start`. Everything else on the event (attendees, recurrence, alarms, time zones, overrides, other properties) is left as it is. On iCloud, an update of a recurring event changes the whole series and is refused without `scope: "series"`. An event that carries both `DTEND` and `DURATION` (not allowed by RFC 5545, but written by some producers) is read by its `DTEND`; an update drops the `DURATION`, so the end the event showed is kept.

**All-day events.** On create, set `all_day` and give `start` and `end` as dates (`YYYY-MM-DD`; RFC3339 is accepted too). The date written is the one on the value's own wall clock, never converted to UTC. `end` is exclusive: a one-day event on 2026-11-26 ends 2026-11-27. On update, an all-day event stays all-day on both providers: only the date of each new time's own wall clock is used, and the end must fall on a later date.

**Recurring events.** Google expands series itself. On iCloud, freestone expands them in the event's own time zone, so a weekly 09:00 meeting stays at 09:00 local across a daylight-saving change and `BYDAY` matches the local date. Moved instances (`RECURRENCE-ID`) replace the instance they move, and `EXDATE` and `RDATE` are applied; at most 366 instances are expanded per series. An object freestone cannot expand is left out and named in `warnings` rather than shown wrongly.

Update and delete address occurrences differently on the two providers:

| | Google | iCloud |
|---|---|---|
| `uid` of a listed occurrence | the occurrence's own id | the series UID, shared by every occurrence |
| Update or delete without `scope` | changes or removes that one occurrence | refused for a recurring event, with an error naming the series |
| `scope: "series"` | not accepted | update or delete the whole series |
| `scope: "instance"` | not accepted | delete only, with `recurrence_id` from `calendar_list_events`: removes that one occurrence. An update with `"instance"` is refused, since single-occurrence edits are not supported on iCloud |

On iCloud, a series update that moves `start` is exact or refused. It moves every occurrence by the same offset on the series' own wall clock (in its time zone, so a 09:00 to 14:30 move gives 14:30 local on both sides of a daylight-saving change): `DTSTART`, every `RDATE`, every `EXDATE`, every moved occurrence's `RECURRENCE-ID` and the rule's `UNTIL` end, each in its own form (zoned, UTC or floating). A value written as a date (`VALUE=DATE`, or `UNTIL=20261130` on a timed series) moves by the offset's whole days only, since a date has no time of day. Moved occurrences stay single, cancelled ones stay cancelled and extra dates move with the series. The other rule parts are not rewritten, so freestone then expands the series before and after the move and accepts it only if every new occurrence is an old one moved by that offset, checked occurrence by occurrence to the series' end, and every moved override and exclusion still names an occurrence. At most 100,000 occurrences are checked: a series without an end (an `RRULE` with neither `UNTIL` nor `COUNT`) is accepted when its first 100,000 all follow, and a series that ends (`UNTIL`, `COUNT`) but has more occurrences than that is refused, since its end cannot be checked. Otherwise the update is refused, the error names the first occurrence that would not follow and the rule part that pins it, and nothing is written. A rule part that fixes a day, month, hour or minute (`BYDAY`, `BYMONTHDAY`, `BYMONTH`, `BYHOUR`, ...) therefore refuses a move that would break it: a weekly `BYDAY=TH` series moved to a Friday is refused, while the same series moved from 09:00 to 14:00 on Thursday succeeds. An override or exclusion that names no occurrence even before the move is refused too. Deleting one occurrence adds an `EXDATE` in the form of the series' `DTSTART` (zoned, UTC, floating, or a date for an all-day series) and removes that occurrence's moved copy if it has one; a `recurrence_id` that is not an occurrence of the series is an error. A non-recurring event needs no `scope`; `"series"` is accepted on it and `"instance"` is an error. On Google, `scope` and `recurrence_id` are errors: an occurrence is addressed by its own `uid`.

**Time zones (iCloud).** A `TZID` may be an IANA name or a Windows zone name as Outlook and Exchange write it (e.g. `W. Europe Standard Time`, mapped through the CLDR table in `internal/calendar/providers/icloud/tzdata/`). An unknown `TZID` fails that object, which is then named in `warnings`. Floating times (no zone) are read in the local time zone of the machine freestone runs on. iCal text values (`SUMMARY`, `DESCRIPTION`, `LOCATION`, `STATUS`) are unescaped; a malformed escape fails the object.

**Events created by other apps (iCloud).** Get, update and delete find an event by its UID, not by an assumed `<uid>.ics` path, so events created by Calendar.app, an iPhone or an invitation are found. If more than one object carries the UID, the call fails and names each href. freestone writes new events at `<calendar>/<uid>.ics`.

**ETags and conflicts.** Both providers return an `etag` on every event, unquoted. Pass it back exactly as received as the `etag` input of `calendar_update_event` or `calendar_delete_event`: the call then succeeds only if the event has not changed since it was read, and otherwise fails with an error saying to re-fetch the event and retry with its current etag. Without an `etag`, a Google update or delete is unconditional; an iCloud update or delete is still conditional on the version freestone has just read, so a change made in between is not overwritten.

### Mail

| Tool | Description |
|------|-------------|
| `mail_list_mailboxes` | List mailboxes/folders/labels. Omit `provider` to list all. |
| `mail_list_messages` | List messages with optional filters (`since`, `before`, `unseen`, `from`, `subject`, `limit`; default 50, cap 200). Newest first on both providers. |
| `mail_get_message` | Get full message content: headers, body text and HTML, and `warnings`. Does not mark the message read. |
| `mail_save_draft_reply` | Save a draft (optionally a reply) to the Drafts folder. **Does not send.** |
| `mail_mark_read` | Mark a message as read. The only tool that changes read state. |

**Reading (`mail_get_message`).** Reading a message leaves its read state unchanged on both providers (iCloud fetches with `BODY.PEEK`). Text is decoded from any charset Go's message library supports (ISO-8859-1, Windows-1252 and others), in the body and in encoded header words. Both providers decode through one shared RFC 5322 decoder; Gmail messages are fetched in raw form. `headers` holds the first value of each header. The body text and HTML are the first inline `text/plain` and `text/html` parts. A part in an unsupported charset is returned as raw text and named in the optional `warnings` list. A message that cannot be parsed, or comes back with no body, is an error, not an empty body.

**Drafts (`mail_save_draft_reply`).** The call fails, and saves nothing, when:
- an address in `to`, `cc` or `bcc` does not parse as a mail address or contains CR or LF
- the subject contains CR or LF
- the parent message (`in_reply_to_id`) is missing, has no `Message-ID`, or carries a malformed `Message-ID` or `References`
- on iCloud, no sender address is available (see below)

A reply carries `In-Reply-To` and `References` (the parent's `References` plus its `Message-ID`), and on Gmail joins the parent's thread. The subject is RFC 2047 encoded where it is not plain ASCII. The body is `text/plain`, `text/html` or `multipart/alternative`, depending on which of `body_text` and `body_html` are given. On iCloud the draft is saved with the `\Draft` flag, and its `From` is `mail.icloud.from_address` from the config, or else `username` if that is a mail address; if neither is, the call fails naming the setting.

**Listing.** iCloud lists newest first (by UID), like Gmail; `limit` keeps the newest. Gmail fetches message metadata with up to 8 requests at once; the first failure cancels the rest and fails the call.

### Files

| Tool | Description |
|------|-------------|
| `files_list_dir` | List directory contents (only paths allowed by access rules). |
| `files_read_file` | Read file contents. Returns text (UTF-8) or base64 (binary). |
| `files_stat` | Get file or directory metadata. |
| `files_glob` | Find files matching a glob pattern (only allowed paths returned). |
| `files_write_file` | Write text content to a file. Requires explicit `overwrite` flag for existing files. |
| `files_make_dir` | Create a directory (and missing parents). Only allowed paths accepted. |

## Audit Log

The write tools (calendar create/update/delete, mail draft save, mail mark read, file write, mkdir) call an append-only JSONL audit logger (`internal/audit`) that records the tool name, provider, input parameters, outcome (ok/error) and timestamp. **`freestone-mcp serve` does not create that logger yet**: `server.audit_file` in the config is read but unused, so no audit log is written today.

When wired, the audit log is best-effort: write failures do not block tool execution.

## File Access Rules

The `access.rules` file controls which paths Claude can access. See `example.access.rules` for syntax.

- `+ /path` allows access to a path and everything below it
- `- /path` denies access
- Rules are evaluated in order; last match wins
- Default is deny (no match = no access)

## Architecture

```
cmd/freestone-mcp          Entry point, wiring, auth subcommand
internal/mcpsrv            MCP adapter layer (thin: MCP <-> domain)
internal/calendar           Domain types, Provider interface, Registry
  providers/icloud          CalDAV driver with RRULE expansion
  providers/google          Google Calendar API v3 driver
internal/mail              Domain types, Provider interface, Registry
  providers/icloud          IMAP driver
  providers/google          Gmail API v1 driver
internal/files             Sandboxed filesystem with rules evaluator
internal/audit             Append-only JSONL audit logger for write operations
internal/config            TOML config loader with validation
internal/secrets           Keychain + env-var secret resolver
internal/gauth             Google OAuth2 helper (flow, token persistence)
internal/logger            Multi-handler slog (JSON file + text stderr)
```

## Known Limitations

- No SMTP send — mail write support saves drafts only (by design, for safety)
- One account per provider
- No connection pooling for IMAP (new connection per operation)
- No pagination: `mail_list_messages` returns at most 200 messages
- macOS only (Keychain dependency)
- Attendee participation status (accepted/declined) is not reported; `attendees` is a list of addresses
- iCloud: `VTIMEZONE` definitions are not evaluated; a `TZID` must be an IANA or Windows zone name
- iCloud: `RECURRENCE-ID;RANGE=THISANDFUTURE` and `RDATE;VALUE=PERIOD` are not supported; such an object is named in `warnings`
- Google OAuth token stored as JSON in Keychain; if refresh token rotation fails, re-run `freestone-mcp auth google`

## Verified against the live services

The tests run against in-process stand-ins for CalDAV, IMAP and the Google APIs, not the real services. Against the real iCloud account, on 2026-11-26:

- iCloud accepted the quoted `If-Match` header on an event update.
- After commit `ee1d486`, `calendar_list_events` returned whole events (before it, iCloud listing returned events with no UID, title or times).
- An event created in Calendar.app was read, updated and deleted through freestone.
- An update with a stale `etag` was refused with the error saying to re-fetch the event.

Not yet verified live: a stale-etag conflict on Google, and the recurring-event `scope` and `recurrence_id` behaviour on iCloud.
