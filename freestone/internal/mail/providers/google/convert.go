package google

import (
	"encoding/base64"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"

	domain "github.com/kai/freestone/internal/mail"
	"github.com/kai/freestone/internal/mail/rfc5322"
)

// messageToSummary converts a Gmail metadata-format message to a domain MessageSummary.
func messageToSummary(msg *gmail.Message) domain.MessageSummary {
	s := domain.MessageSummary{
		Provider: "google",
		ID:       msg.Id,
		Size:     uint32(msg.SizeEstimate),
	}

	// Extract label-based flags.
	for _, label := range msg.LabelIds {
		s.Flags = append(s.Flags, label)
		if label == "INBOX" && s.MailboxID == "" {
			s.MailboxID = "INBOX"
		}
	}
	if s.MailboxID == "" && len(msg.LabelIds) > 0 {
		s.MailboxID = msg.LabelIds[0]
	}

	// Parse headers.
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "subject":
				s.Subject = h.Value
			case "from":
				s.From = h.Value
			case "to":
				s.To = splitAddresses(h.Value)
			case "date":
				if t, err := parseDate(h.Value); err == nil {
					s.Date = t
				}
			}
		}
	}

	// Fallback: use internalDate if no Date header parsed.
	if s.Date.IsZero() && msg.InternalDate > 0 {
		s.Date = time.UnixMilli(msg.InternalDate)
	}

	return s
}

// messageFromFull converts a Gmail full-format message to a domain Message.
func messageFromFull(msg *gmail.Message) domain.Message {
	m := domain.Message{
		MessageSummary: messageToSummary(msg),
		Headers:        make(map[string]string),
	}

	if msg.Payload != nil {
		// Capture all headers.
		for _, h := range msg.Payload.Headers {
			m.Headers[h.Name] = h.Value
		}

		// Extract body parts.
		m.Text, m.HTML = extractBodyParts(msg.Payload)
	}

	return m
}

// extractBodyParts recursively extracts text/plain and text/html parts.
func extractBodyParts(part *gmail.MessagePart) (text, html string) {
	if part == nil {
		return "", ""
	}

	mime := strings.ToLower(part.MimeType)

	// Leaf node with body data.
	if part.Body != nil && part.Body.Data != "" {
		decoded := decodeBase64URL(part.Body.Data)
		switch {
		case mime == "text/plain" && text == "":
			return decoded, ""
		case mime == "text/html" && html == "":
			return "", decoded
		}
	}

	// Multipart: recurse into sub-parts.
	for _, sub := range part.Parts {
		t, h := extractBodyParts(sub)
		if t != "" && text == "" {
			text = t
		}
		if h != "" && html == "" {
			html = h
		}
		if text != "" && html != "" {
			break
		}
	}

	return text, html
}

// decodeBase64URL decodes a base64url-encoded string (no padding).
func decodeBase64URL(s string) string {
	data, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(s)
	if err != nil {
		return ""
	}
	return string(data)
}

// splitAddresses parses an RFC 5322 address list header value.
// Falls back to naive comma-split if parsing fails (e.g. malformed headers).
func splitAddresses(s string) []string {
	if s == "" {
		return nil
	}
	parsed, err := mail.ParseAddressList(s)
	if err == nil {
		addrs := make([]string, len(parsed))
		for i, a := range parsed {
			addrs[i] = a.String()
		}
		return addrs
	}
	// Fallback: naive split for headers that net/mail cannot parse.
	parts := strings.Split(s, ",")
	addrs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			addrs = append(addrs, p)
		}
	}
	return addrs
}

// replyThreading reads the Message-ID and References of a metadata-format
// message. A parent without a Message-ID cannot be threaded on, which is an
// error rather than a silently unthreaded reply.
func replyThreading(msg *gmail.Message) (messageID string, references []string, err error) {
	var rawID, rawRefs string
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			switch strings.ToLower(h.Name) {
			case "message-id":
				rawID = h.Value
			case "references":
				rawRefs = h.Value
			}
		}
	}
	ids, err := rfc5322.ParseMsgIDList(rawID)
	if err != nil {
		return "", nil, fmt.Errorf("parse Message-ID: %w", err)
	}
	if len(ids) != 1 {
		return "", nil, fmt.Errorf("want exactly one Message-ID to thread on, found %d (%q)", len(ids), rawID)
	}
	references, err = rfc5322.ParseMsgIDList(rawRefs)
	if err != nil {
		return "", nil, fmt.Errorf("parse References: %w", err)
	}
	return ids[0], references, nil
}

// parseDate attempts to parse common email date formats.
func parseDate(s string) (time.Time, error) {
	formats := []string{
		"Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST",
		"2 Jan 2006 15:04:05 -0700",
		time.RFC1123Z,
		time.RFC1123,
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, &time.ParseError{Value: s}
}
