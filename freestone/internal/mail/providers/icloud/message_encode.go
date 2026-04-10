package icloud

import (
	"bytes"
	"fmt"
	"mime"
	"net/textproto"
	"strings"
	"time"

	"github.com/kai/freestone/internal/mail"
)

// encodeDraftReply builds an RFC 5322 message suitable for IMAP APPEND into
// a Drafts folder. It sets In-Reply-To and References headers for threading
// when inReplyToMsgID is provided.
func encodeDraftReply(d mail.DraftReply, inReplyToMsgID string) ([]byte, error) {
	var buf bytes.Buffer

	// Headers
	writeHeader(&buf, "From", joinAddresses(d.To[:0])) // From will be set by mail client on send
	writeHeader(&buf, "To", strings.Join(d.To, ", "))
	if len(d.Cc) > 0 {
		writeHeader(&buf, "Cc", strings.Join(d.Cc, ", "))
	}
	if len(d.Bcc) > 0 {
		writeHeader(&buf, "Bcc", strings.Join(d.Bcc, ", "))
	}
	writeHeader(&buf, "Subject", encodeSubject(d.Subject))
	writeHeader(&buf, "Date", time.Now().UTC().Format(time.RFC1123Z))
	writeHeader(&buf, "MIME-Version", "1.0")

	if inReplyToMsgID != "" {
		writeHeader(&buf, "In-Reply-To", inReplyToMsgID)
		writeHeader(&buf, "References", inReplyToMsgID)
	}

	// Body — use multipart/alternative if both text and HTML are present.
	if d.BodyHTML != "" && d.BodyText != "" {
		boundary := "freestone-boundary-" + fmt.Sprintf("%d", time.Now().UnixNano())
		writeHeader(&buf, "Content-Type", fmt.Sprintf("multipart/alternative; boundary=%q", boundary))
		buf.WriteString("\r\n")

		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(d.BodyText)
		buf.WriteString("\r\n")

		buf.WriteString("--" + boundary + "\r\n")
		buf.WriteString("Content-Type: text/html; charset=utf-8\r\n")
		buf.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		buf.WriteString(d.BodyHTML)
		buf.WriteString("\r\n")

		buf.WriteString("--" + boundary + "--\r\n")
	} else {
		writeHeader(&buf, "Content-Type", "text/plain; charset=utf-8")
		writeHeader(&buf, "Content-Transfer-Encoding", "8bit")
		buf.WriteString("\r\n")
		body := d.BodyText
		if body == "" {
			body = d.BodyHTML
		}
		buf.WriteString(body)
		buf.WriteString("\r\n")
	}

	return buf.Bytes(), nil
}

func writeHeader(buf *bytes.Buffer, key, value string) {
	buf.WriteString(textproto.CanonicalMIMEHeaderKey(key))
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteString("\r\n")
}

func encodeSubject(s string) string {
	if s == "" {
		return ""
	}
	return mime.QEncoding.Encode("utf-8", s)
}

func joinAddresses(addrs []string) string {
	return strings.Join(addrs, ", ")
}
