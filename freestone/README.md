# freestone

A personal MCP server that extends Claude Code with calendar, mail, and file access tools. Runs locally on macOS, speaks MCP over stdio.

## Status

**Phase 2** — Read/write with iCloud and Google providers. All write operations are audited.

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

This opens your browser for Google consent. Phase 2 requests read/write scopes:
- `https://www.googleapis.com/auth/calendar` (read + create/update/delete events)
- `https://www.googleapis.com/auth/gmail.modify` (read + draft save + mark read)

The refresh token is stored in Keychain under the `token_ref` from your config (default: `freestone-google-token`).

To revoke access and delete the stored token:

```bash
freestone-mcp auth google --revoke
```

### 4. Configure Claude Code

Add freestone to your Claude Code MCP configuration (`~/.config/claude/mcp.json`):

```json
{
  "mcpServers": {
    "freestone": {
      "command": "/Users/YOU/.local/bin/freestone-mcp",
      "args": ["serve"]
    }
  }
}
```

## Tools

The `provider` parameter selects which backend to use (`icloud` or `google`). Omit it on list operations to aggregate results from all enabled providers. All write operations are recorded in the audit log.

### Calendar

| Tool | Description |
|------|-------------|
| `calendar_list_calendars` | List calendars. Omit `provider` to list from all enabled providers. |
| `calendar_list_events` | List events in a calendar within a time range (RFC3339 `start`/`end`). |
| `calendar_get_event` | Get a single event by UID. |
| `calendar_create_event` | Create a new event (summary, start, end, optional description/location). |
| `calendar_update_event` | Update an existing event by UID. Supports partial updates. |
| `calendar_delete_event` | Delete an event by UID. Supports ETag for conflict detection (iCloud). |

### Mail

| Tool | Description |
|------|-------------|
| `mail_list_mailboxes` | List mailboxes/folders/labels. Omit `provider` to list all. |
| `mail_list_messages` | List messages with optional filters (`since`, `before`, `unseen`, `from`, `subject`, `limit`). |
| `mail_get_message` | Get full message content including body text and HTML. |
| `mail_save_draft_reply` | Save a draft reply to the Drafts folder. **Does not send.** Supports In-Reply-To threading. |
| `mail_mark_read` | Mark a message as read. |

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

All write operations (calendar create/update/delete, mail draft save, mail mark read, file write, mkdir) are recorded in an append-only JSONL audit log at `~/.config/freestone/audit.jsonl`. Each entry records the tool name, provider, input parameters, outcome (ok/error), and timestamp.

The audit log is best-effort — write failures do not block tool execution.

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
- macOS only (Keychain dependency)
- iCloud CalDAV does not enforce ETag-based conflict detection (upstream library limitation)
- Google OAuth token stored as JSON in Keychain; if refresh token rotation fails, re-run `freestone-mcp auth google`
