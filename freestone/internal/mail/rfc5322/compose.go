package rfc5322

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	netmail "net/mail"
	"strings"
	"time"

	"github.com/emersion/go-message/mail"
	"github.com/emersion/go-message/textproto"
)

// Draft is the content of a draft message, provider-neutral.
type Draft struct {
	// From is the sender address. Empty omits the field, for a provider
	// that fills it in itself (Gmail).
	From        string
	To, Cc, Bcc []string
	Subject     string
	Text, HTML  string
	// ParentMessageID is the Message-ID of the message replied to, without
	// angle brackets. Empty means the draft is not a reply.
	ParentMessageID string
	// ParentReferences is the parent's own References chain, identifiers
	// without angle brackets, oldest first.
	ParentReferences []string
}

// ComposeDraft encodes d as an RFC 5322 message with CRLF line endings.
//
// Every header value that comes from tool input is checked before it is
// written: an address that net/mail cannot parse, or a CR or LF anywhere in
// a header value, is an error, so input cannot add header fields of its own.
// The subject is RFC 2047 encoded where it is not plain ASCII. A reply
// carries In-Reply-To (the parent's Message-ID) and References (the parent's
// References followed by its Message-ID, RFC 5322 section 3.6.4).
func ComposeDraft(d Draft) ([]byte, error) {
	var h mail.Header
	h.SetDate(time.Now())

	if d.From != "" {
		from, err := parseAddress("From", d.From)
		if err != nil {
			return nil, err
		}
		h.SetAddressList("From", []*mail.Address{from})
	}
	for _, field := range []struct {
		key   string
		addrs []string
	}{{"To", d.To}, {"Cc", d.Cc}, {"Bcc", d.Bcc}} {
		list, err := parseAddressList(field.key, field.addrs)
		if err != nil {
			return nil, err
		}
		h.SetAddressList(field.key, list)
	}

	if err := rejectLineBreaks("Subject", d.Subject); err != nil {
		return nil, err
	}
	h.SetSubject(d.Subject)

	if err := setThreading(&h, d.ParentMessageID, d.ParentReferences); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := writeBody(&buf, h, d.Text, d.HTML); err != nil {
		return nil, fmt.Errorf("rfc5322: write draft: %w", err)
	}
	return buf.Bytes(), nil
}

// ParseMsgIDList parses a Message-ID, In-Reply-To or References field value
// into identifiers without angle brackets. An empty value is an empty list.
// A value still folded across lines is unfolded first.
func ParseMsgIDList(value string) ([]string, error) {
	var h mail.Header
	h.Set("References", unfold.Replace(value))
	ids, err := h.MsgIDList("References")
	if err != nil {
		return nil, fmt.Errorf("rfc5322: parse message identifiers %q: %w", value, err)
	}
	return ids, nil
}

// HeaderReferences reads a raw header block, such as an IMAP
// BODY[HEADER.FIELDS (REFERENCES)] section, and returns its References
// identifiers. A block without the field yields an empty list.
func HeaderReferences(header []byte) ([]string, error) {
	th, err := textproto.ReadHeader(bufio.NewReader(bytes.NewReader(header)))
	if err != nil {
		return nil, fmt.Errorf("rfc5322: read header block: %w", err)
	}
	return ParseMsgIDList(th.Get("References"))
}

// unfold removes the line breaks of a folded field value; the whitespace
// that starts each continuation line stays as the separator.
var unfold = strings.NewReplacer("\r\n", "", "\n", "", "\r", "")

func setThreading(h *mail.Header, parentID string, parentRefs []string) error {
	if parentID == "" {
		if len(parentRefs) > 0 {
			return errors.New("rfc5322: parent References given without the parent Message-ID")
		}
		return nil
	}
	refs := make([]string, 0, len(parentRefs)+1)
	refs = append(refs, parentRefs...)
	refs = append(refs, parentID)
	for _, id := range refs {
		if err := checkMsgID(id); err != nil {
			return err
		}
	}
	h.SetMsgIDList("In-Reply-To", []string{parentID})
	h.SetMsgIDList("References", refs)
	return nil
}

// checkMsgID rejects an identifier that would not survive being written
// between angle brackets as a single msg-id.
func checkMsgID(id string) error {
	if id == "" || strings.ContainsAny(id, "<> \t\r\n") {
		return fmt.Errorf("rfc5322: invalid message identifier %q", id)
	}
	return nil
}

func parseAddressList(field string, addrs []string) ([]*mail.Address, error) {
	list := make([]*mail.Address, 0, len(addrs))
	for _, a := range addrs {
		addr, err := parseAddress(field, a)
		if err != nil {
			return nil, err
		}
		list = append(list, addr)
	}
	return list, nil
}

// parseAddress accepts exactly one address per value: a comma-separated
// list in a single entry fails net/mail.ParseAddress as well.
func parseAddress(field, value string) (*mail.Address, error) {
	if err := rejectLineBreaks(field, value); err != nil {
		return nil, err
	}
	addr, err := netmail.ParseAddress(value)
	if err != nil {
		return nil, fmt.Errorf("rfc5322: invalid %s address %q: %w", field, value, err)
	}
	return addr, nil
}

func rejectLineBreaks(field, value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("rfc5322: %s contains a line break (CR or LF), which would inject header fields", field)
	}
	return nil
}

// writeBody writes h and the body. Text-only and HTML-only drafts are a
// single part labelled with their own type; both together are
// multipart/alternative. Quoted-printable, which go-message picks for text
// parts, turns every line break into CRLF.
func writeBody(w io.Writer, h mail.Header, text, html string) error {
	if text != "" && html != "" {
		iw, err := mail.CreateInlineWriter(w, h)
		if err != nil {
			return err
		}
		if err := writeInlinePart(iw, "text/plain", text); err != nil {
			return err
		}
		if err := writeInlinePart(iw, "text/html", html); err != nil {
			return err
		}
		return iw.Close()
	}

	mediaType, body := "text/plain", text
	if html != "" {
		mediaType, body = "text/html", html
	}
	h.SetContentType(mediaType, map[string]string{"charset": "utf-8"})
	pw, err := mail.CreateSingleInlineWriter(w, h)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(pw, body); err != nil {
		return err
	}
	return pw.Close()
}

func writeInlinePart(iw *mail.InlineWriter, mediaType, body string) error {
	var ph mail.InlineHeader
	ph.SetContentType(mediaType, map[string]string{"charset": "utf-8"})
	pw, err := iw.CreatePart(ph)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(pw, body); err != nil {
		return err
	}
	return pw.Close()
}
