package google

import (
	"bytes"
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
	s := summaryFromLabels(msg)
	if msg.Payload != nil {
		for _, h := range msg.Payload.Headers {
			applySummaryHeader(&s, h.Name, h.Value)
		}
	}
	fallBackToInternalDate(&s, msg)
	return s
}

// messageFromRaw converts a Gmail raw-format message to a domain Message.
// The raw bytes go through the shared rfc5322 decoder, so a Gmail message
// decodes exactly as the same message fetched over IMAP would. A message
// that cannot be decoded is an error, never an empty body.
func messageFromRaw(msg *gmail.Message) (domain.Message, error) {
	if msg.Raw == "" {
		return domain.Message{}, fmt.Errorf("message %s: server returned no raw content", msg.Id)
	}
	data, err := decodeRaw(msg.Raw)
	if err != nil {
		return domain.Message{}, fmt.Errorf("message %s: decode raw field: %w", msg.Id, err)
	}
	body, err := rfc5322.Decode(bytes.NewReader(data))
	if err != nil {
		return domain.Message{}, fmt.Errorf("message %s: %w", msg.Id, err)
	}

	s := summaryFromLabels(msg)
	for name, value := range body.Headers {
		applySummaryHeader(&s, name, value)
	}
	fallBackToInternalDate(&s, msg)

	return domain.Message{
		MessageSummary: s,
		Headers:        body.Headers,
		Text:           body.Text,
		HTML:           body.HTML,
		Warnings:       body.Warnings,
	}, nil
}

// decodeRaw decodes Gmail's raw field. The API documents it as base64url;
// whether it is padded is not specified, so both forms are accepted.
func decodeRaw(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// summaryFromLabels starts a summary from the fields Gmail reports outside
// the message itself: id, size and labels (as flags and mailbox).
func summaryFromLabels(msg *gmail.Message) domain.MessageSummary {
	s := domain.MessageSummary{
		Provider: "google",
		ID:       msg.Id,
		Size:     uint32(msg.SizeEstimate),
	}
	for _, label := range msg.LabelIds {
		s.Flags = append(s.Flags, label)
		if label == "INBOX" && s.MailboxID == "" {
			s.MailboxID = "INBOX"
		}
	}
	if s.MailboxID == "" && len(msg.LabelIds) > 0 {
		s.MailboxID = msg.LabelIds[0]
	}
	return s
}

// applySummaryHeader sets the summary field a header carries, if any.
func applySummaryHeader(s *domain.MessageSummary, name, value string) {
	switch strings.ToLower(name) {
	case "subject":
		s.Subject = value
	case "from":
		s.From = value
	case "to":
		s.To = splitAddresses(value)
	case "date":
		if t, err := parseDate(value); err == nil {
			s.Date = t
		}
	}
}

// fallBackToInternalDate uses Gmail's internalDate when no Date header parsed.
func fallBackToInternalDate(s *domain.MessageSummary, msg *gmail.Message) {
	if s.Date.IsZero() && msg.InternalDate > 0 {
		s.Date = time.UnixMilli(msg.InternalDate)
	}
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
