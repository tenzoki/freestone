package google

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/api/gmail/v1"
)

// GetMessage fetches format=raw and decodes the bytes through the shared
// rfc5322 codec, so charsets, padded and unpadded base64url and parts the
// "full" format would only reference by attachment id all come back right.

const latin1QuotedPrintable = "From: =?ISO-8859-1?Q?J=FCrgen?= <juergen@example.com>\r\n" +
	"To: kai@example.com\r\n" +
	"Subject: =?ISO-8859-1?Q?Gr=FC=DFe?=\r\n" +
	"Date: Tue, 3 Nov 2026 10:15:00 +0100\r\n" +
	"Message-ID: <latin1@example.com>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=ISO-8859-1\r\n" +
	"Content-Transfer-Encoding: quoted-printable\r\n" +
	"\r\n" +
	"Gr=FC=DFe aus M=FCnchen\r\n"

// windows1252Alternative is 8bit Windows-1252: 0xFC is ü, 0xDF is ß, 0x80 is €.
const windows1252Alternative = "From: alice@example.com\r\n" +
	"To: kai@example.com\r\n" +
	"Subject: Rechnung\r\n" +
	"Date: Tue, 3 Nov 2026 11:00:00 +0100\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
	"\r\n" +
	"--b1\r\n" +
	"Content-Type: text/plain; charset=windows-1252\r\n" +
	"Content-Transfer-Encoding: 8bit\r\n" +
	"\r\n" +
	"Sch\xf6ne Gr\xfc\xdfe, 10 \x80\r\n" +
	"--b1\r\n" +
	"Content-Type: text/html; charset=windows-1252\r\n" +
	"Content-Transfer-Encoding: 8bit\r\n" +
	"\r\n" +
	"<p>Gr\xfc\xdfe \x80</p>\r\n" +
	"--b1--\r\n"

func serveRaw(h *gmailAPIHarness, id, raw string) {
	h.respond(http.MethodGet, gmailMessagePath(id), http.StatusOK, &gmail.Message{
		Id:           id,
		ThreadId:     "t-" + id,
		LabelIds:     []string{"INBOX", "UNREAD"},
		SizeEstimate: int64(len(raw)),
		Raw:          raw,
	})
}

func TestGetMessage_RequestsRawFormat(t *testing.T) {
	h := newGmailAPIHarness(t)
	serveRaw(h, "m-1", base64.RawURLEncoding.EncodeToString([]byte(latin1QuotedPrintable)))

	if _, err := h.driver().GetMessage(context.Background(), "INBOX", "m-1"); err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	req := h.only()
	if req.Path != gmailMessagePath("m-1") || req.Query.Get("format") != "raw" {
		t.Errorf("request = %s %v, want %s with format=raw", req.Path, req.Query, gmailMessagePath("m-1"))
	}
}

func TestGetMessage_Latin1QuotedPrintable(t *testing.T) {
	h := newGmailAPIHarness(t)
	serveRaw(h, "m-1", base64.RawURLEncoding.EncodeToString([]byte(latin1QuotedPrintable)))

	m, err := h.driver().GetMessage(context.Background(), "INBOX", "m-1")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Text != "Grüße aus München\r\n" {
		t.Errorf("Text = %q, want %q", m.Text, "Grüße aus München\r\n")
	}
	if m.Subject != "Grüße" {
		t.Errorf("Subject = %q, want Grüße", m.Subject)
	}
	if m.From != "Jürgen <juergen@example.com>" {
		t.Errorf("From = %q, want Jürgen <juergen@example.com>", m.From)
	}
	if len(m.To) != 1 || m.To[0] != "<kai@example.com>" {
		t.Errorf("To = %v, want [<kai@example.com>]", m.To)
	}
	if want := time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC); !m.Date.Equal(want) {
		t.Errorf("Date = %v, want %v", m.Date, want)
	}
	if m.Headers["Message-ID"] != "<latin1@example.com>" {
		t.Errorf("Headers[Message-ID] = %q; headers = %v", m.Headers["Message-ID"], m.Headers)
	}
	if m.Provider != "google" || m.ID != "m-1" || m.MailboxID != "INBOX" {
		t.Errorf("summary = %+v, want google/m-1 in INBOX", m.MessageSummary)
	}
	if len(m.Flags) != 2 || m.Flags[1] != "UNREAD" {
		t.Errorf("Flags = %v, want [INBOX UNREAD]", m.Flags)
	}
	if len(m.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", m.Warnings)
	}
}

func TestGetMessage_Windows1252Alternative(t *testing.T) {
	h := newGmailAPIHarness(t)
	serveRaw(h, "m-2", base64.RawURLEncoding.EncodeToString([]byte(windows1252Alternative)))

	m, err := h.driver().GetMessage(context.Background(), "INBOX", "m-2")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Text != "Schöne Grüße, 10 €" {
		t.Errorf("Text = %q, want %q", m.Text, "Schöne Grüße, 10 €")
	}
	if m.HTML != "<p>Grüße €</p>" {
		t.Errorf("HTML = %q, want %q", m.HTML, "<p>Grüße €</p>")
	}
}

func TestGetMessage_PaddedRawDecodes(t *testing.T) {
	raw := base64.URLEncoding.EncodeToString([]byte(latin1QuotedPrintable))
	if !strings.HasSuffix(raw, "=") {
		t.Fatalf("fixture does not exercise padding: %q", raw[len(raw)-4:])
	}
	h := newGmailAPIHarness(t)
	serveRaw(h, "m-3", raw)

	m, err := h.driver().GetMessage(context.Background(), "INBOX", "m-3")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Text != "Grüße aus München\r\n" {
		t.Errorf("Text = %q, want %q", m.Text, "Grüße aus München\r\n")
	}
}

// Gmail's "full" format moves a large body part out of line behind an
// attachmentId; the raw format carries it inline like any other part.
func TestGetMessage_LargeTextPart(t *testing.T) {
	big := strings.Repeat("Zeile mit Text.\r\n", 4096) // ~70 KB
	msg := "From: alice@example.com\r\n" +
		"Subject: Gross\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + big
	h := newGmailAPIHarness(t)
	serveRaw(h, "m-4", base64.RawURLEncoding.EncodeToString([]byte(msg)))

	m, err := h.driver().GetMessage(context.Background(), "INBOX", "m-4")
	if err != nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if m.Text != big {
		t.Errorf("Text has %d bytes, want %d", len(m.Text), len(big))
	}
}

func TestGetMessage_UnparseableIsError(t *testing.T) {
	cases := map[string]string{
		"raw is not base64url": "!!!not base64!!!",
		"malformed header":     base64.RawURLEncoding.EncodeToString([]byte("this line has no colon\r\n\r\nbody\r\n")),
		"no raw content":       "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			h := newGmailAPIHarness(t)
			serveRaw(h, "m-5", raw)

			m, err := h.driver().GetMessage(context.Background(), "INBOX", "m-5")
			if err == nil {
				t.Fatalf("GetMessage = %+v, want an error", m)
			}
			if !strings.Contains(err.Error(), "m-5") {
				t.Errorf("error %q does not name the message", err)
			}
		})
	}
}
