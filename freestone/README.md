# freestone

A personal MCP server that extends Claude Code with calendar, mail, and file access tools. Runs locally on macOS, speaks MCP over stdio.

## Status

**Phase 1A** — Read-only MVP with iCloud providers.

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

### 2. iCloud app-specific password

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

### 3. Configure Claude Code

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

## Tools (Phase 1A)

All tools are read-only in Phase 1A.

### Calendar

| Tool | Description |
|------|-------------|
| `calendar_list_calendars` | List calendars. Omit `provider` to list from all enabled providers. |
| `calendar_list_events` | List events in a calendar within a time range (RFC3339 `start`/`end`). |
| `calendar_get_event` | Get a single event by UID. |

### Mail

| Tool | Description |
|------|-------------|
| `mail_list_mailboxes` | List mailboxes/folders. Omit `provider` to list all. |
| `mail_list_messages` | List messages with optional filters (`since`, `before`, `unseen`, `from`, `subject`, `limit`). |
| `mail_get_message` | Get full message content including body text and HTML. |

### Files

| Tool | Description |
|------|-------------|
| `files_list_dir` | List directory contents (only paths allowed by access rules). |
| `files_read_file` | Read file contents. Returns text (UTF-8) or base64 (binary). |
| `files_stat` | Get file or directory metadata. |
| `files_glob` | Find files matching a glob pattern (only allowed paths returned). |

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
internal/mail              Domain types, Provider interface, Registry
internal/files             Sandboxed filesystem with rules evaluator
internal/config            TOML config loader with validation
internal/secrets           Keychain + env-var secret resolver
internal/logger            Multi-handler slog (JSON file + text stderr)
```

## Known Limitations

- iCloud only (Google providers planned for Phase 1B)
- Read-only (writes planned for Phase 2)
- No SMTP send — drafts only (Phase 2)
- One account per provider
- No connection pooling for IMAP (new connection per operation)
- macOS only (Keychain dependency)
