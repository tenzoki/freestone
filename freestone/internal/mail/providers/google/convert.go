package google

import (
	"encoding/base64"
	"net/mail"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"

	domain "github.com/kai/freestone/internal/mail"
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

// buildRFC5322 creates a minimal RFC 5322 message from a DraftReply.
func buildRFC5322(d domain.DraftReply) []byte {
	var b strings.Builder
	b.WriteString("To: " + strings.Join(d.To, ", ") + "\r\n")
	if len(d.Cc) > 0 {
		b.WriteString("Cc: " + strings.Join(d.Cc, ", ") + "\r\n")
	}
	if len(d.Bcc) > 0 {
		b.WriteString("Bcc: " + strings.Join(d.Bcc, ", ") + "\r\n")
	}
	b.WriteString("Subject: " + d.Subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	body := d.BodyText
	if body == "" {
		body = d.BodyHTML
	}
	b.WriteString(body)
	return []byte(b.String())
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
