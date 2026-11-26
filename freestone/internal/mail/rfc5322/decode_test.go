package rfc5322

import (
	"strings"
	"testing"
)

// In multipart fixtures the CRLF before a boundary belongs to the
// delimiter (RFC 2046 5.1.1), so part bodies decode without it.
//
// The fixtures spell the bytes of each charset explicitly: in ISO-8859-1 and
// Windows-1252, ü is 0xFC and ß is 0xDF; Windows-1252 also has the euro sign
// at 0x80, which ISO-8859-1 lacks.

func decode(t *testing.T, raw string) Body {
	t.Helper()
	b, err := Decode(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	return b
}

func TestDecode_ISO88591QuotedPrintable(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"Subject: test\r\n" +
		"Content-Type: text/plain; charset=ISO-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"Gr=FC=DFe aus M=FCnchen\r\n"

	b := decode(t, raw)
	if b.Text != "Grüße aus München\r\n" {
		t.Errorf("Text = %q, want %q", b.Text, "Grüße aus München\r\n")
	}
	if len(b.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", b.Warnings)
	}
}

func TestDecode_ISO88591Base64(t *testing.T) {
	// base64 of the ISO-8859-1 bytes "Grüße" (47 72 FC DF 65).
	raw := "Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		"R3L832U=\r\n"

	if b := decode(t, raw); b.Text != "Grüße" {
		t.Errorf("Text = %q, want %q", b.Text, "Grüße")
	}
}

func TestDecode_ISO88591Raw8bit(t *testing.T) {
	raw := "Content-Type: text/plain; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"Gr\xfc\xdfe"

	if b := decode(t, raw); b.Text != "Grüße" {
		t.Errorf("Text = %q, want %q", b.Text, "Grüße")
	}
}

func TestDecode_Windows1252InMultipartAlternative(t *testing.T) {
	raw := "From: a@example.com\r\n" +
		"Subject: Rechnung\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=windows-1252\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"Sch=F6ne Gr=FC=DFe, 10 =80\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=windows-1252\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		// base64 of the Windows-1252 bytes "<p>Gr\xfc\xdfe \x80</p>"
		"PHA+R3L832UggDwvcD4=\r\n" +
		"--b1--\r\n"

	b := decode(t, raw)
	if b.Text != "Schöne Grüße, 10 €" {
		t.Errorf("Text = %q, want %q", b.Text, "Schöne Grüße, 10 €")
	}
	if b.HTML != "<p>Grüße €</p>" {
		t.Errorf("HTML = %q, want %q", b.HTML, "<p>Grüße €</p>")
	}
	if len(b.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", b.Warnings)
	}
}

func TestDecode_EncodedWordHeaders(t *testing.T) {
	raw := "From: =?windows-1252?q?J=FCrgen_M=FCller?= <j@example.com>\r\n" +
		"Subject: =?iso-8859-1?q?Gr=FC=DFe?= und =?ISO-8859-1?B?U2No9m5l?=\r\n" +
		"Message-ID: <1@example.com>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"x\r\n"

	b := decode(t, raw)
	if got, want := b.Headers["Subject"], "Grüße und Schöne"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if got, want := b.Headers["From"], "Jürgen Müller <j@example.com>"; got != want {
		t.Errorf("From = %q, want %q", got, want)
	}
	// Header names keep their wire spelling, not go-message's canonical form.
	if got := b.Headers["Message-ID"]; got != "<1@example.com>" {
		t.Errorf("Headers[Message-ID] = %q, want <1@example.com>; headers = %v", got, b.Headers)
	}
}

func TestDecode_RepeatedHeaderKeepsFirstValue(t *testing.T) {
	raw := "Received: first\r\n" +
		"Received: second\r\n" +
		"\r\n" +
		"x\r\n"

	if got := decode(t, raw).Headers["Received"]; got != "first" {
		t.Errorf("Received = %q, want first", got)
	}
}

func TestWordDecoder_Windows1252(t *testing.T) {
	got, err := WordDecoder().DecodeHeader("=?windows-1252?q?Gr=FC=DFe_=80?=")
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}
	if got != "Grüße €" {
		t.Errorf("DecodeHeader = %q, want %q", got, "Grüße €")
	}
}

func TestDecode_UnknownCharsetKeepsBodyAndWarns(t *testing.T) {
	raw := "Content-Type: multipart/alternative; boundary=b1\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=x-unknown\r\n" +
		"\r\n" +
		"raw body\r\n" +
		"--b1\r\n" +
		"Content-Type: text/html; charset=iso-8859-1\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"\r\n" +
		"<p>Gr=FC=DFe</p>\r\n" +
		"--b1--\r\n"

	b := decode(t, raw)
	if b.Text != "raw body" {
		t.Errorf("Text = %q, want the undecoded body", b.Text)
	}
	// The part after the undecodable one is still decoded.
	if b.HTML != "<p>Grüße</p>" {
		t.Errorf("HTML = %q, want %q", b.HTML, "<p>Grüße</p>")
	}
	if len(b.Warnings) != 1 {
		t.Fatalf("Warnings = %v, want exactly one", b.Warnings)
	}
	if w := b.Warnings[0]; !strings.Contains(w, `"x-unknown"`) || !strings.Contains(w, "part 1") {
		t.Errorf("warning = %q, want it to name part 1 and charset x-unknown", w)
	}
}

func TestDecode_UnknownCharsetSinglePart(t *testing.T) {
	raw := "Content-Type: text/plain; charset=x-unknown\r\n" +
		"\r\n" +
		"raw body"

	b := decode(t, raw)
	if b.Text != "raw body" {
		t.Errorf("Text = %q, want the undecoded body", b.Text)
	}
	if len(b.Warnings) != 1 || !strings.Contains(b.Warnings[0], "message body") {
		t.Errorf("Warnings = %v, want one naming the message body", b.Warnings)
	}
}

func TestDecode_AttachmentIsNotBody(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=b1\r\n" +
		"\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"Content-Disposition: attachment; filename=notes.txt\r\n" +
		"\r\n" +
		"attached\r\n" +
		"--b1\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"inline body\r\n" +
		"--b1--\r\n"

	if b := decode(t, raw); b.Text != "inline body" {
		t.Errorf("Text = %q, want the inline part", b.Text)
	}
}

func TestDecode_UnparseableIsError(t *testing.T) {
	cases := map[string]string{
		"malformed header line": "this line has no colon\r\n\r\nbody\r\n",
		"multipart without boundary": "Content-Type: multipart/mixed\r\n\r\n" +
			"--x\r\nContent-Type: text/plain\r\n\r\nhi\r\n--x--\r\n",
		"multipart with no parts": "Content-Type: multipart/mixed; boundary=zz\r\n\r\nno parts here\r\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := Decode(strings.NewReader(raw))
			if err == nil {
				t.Fatalf("Decode = %+v, nil error; want an error", b)
			}
		})
	}
}
