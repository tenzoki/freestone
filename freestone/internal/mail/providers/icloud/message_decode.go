package icloud

import (
	"io"
	"strings"

	gomessage "github.com/emersion/go-message/mail"
)

// decodeBody parses an RFC 5322 message body and returns the plain text
// and HTML parts.
func decodeBody(r io.Reader) (text, html string) {
	mr, err := gomessage.CreateReader(r)
	if err != nil {
		return "", ""
	}

	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}

		switch h := p.Header.(type) {
		case *gomessage.InlineHeader:
			ct, _, _ := h.ContentType()
			body, readErr := io.ReadAll(p.Body)
			if readErr != nil {
				continue
			}
			switch {
			case strings.HasPrefix(ct, "text/plain"):
				text = string(body)
			case strings.HasPrefix(ct, "text/html"):
				html = string(body)
			}
		}
	}

	return text, html
}
