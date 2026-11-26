// Package rfc5322 is freestone's one MIME codec for mail: it decodes raw
// RFC 5322 messages into text, HTML and headers, whichever provider fetched
// the bytes. Every provider driver decodes through this package so that a
// charset or encoded word is handled the same way on every account.
package rfc5322

import (
	"fmt"
	"io"
	"mime"
	"strconv"
	"strings"

	"github.com/emersion/go-message"
	// Registers go-message's charset table (ISO-8859-*, Windows-125x, ...)
	// as message.CharsetReader. Without it go-message decodes only UTF-8
	// and US-ASCII and reports every other charset as unknown.
	"github.com/emersion/go-message/charset"
)

// Body is the decoded content of one message.
type Body struct {
	// Headers holds the top-level header fields, encoded words decoded. A
	// header that occurs more than once keeps its first value.
	Headers map[string]string
	Text    string
	HTML    string
	// Warnings names each part of the returned content that could not be
	// fully decoded and was returned as it stood on the wire instead.
	Warnings []string
}

// WordDecoder returns a decoder for RFC 2047 encoded words that knows the
// same charsets as Decode.
func WordDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: charset.Reader}
}

// Decode parses a raw RFC 5322 message. The first inline text/plain and the
// first inline text/html part become Text and HTML. A part whose charset or
// transfer encoding is unknown is kept as received and named in Warnings. A
// message whose header or MIME structure cannot be parsed is an error, never
// an empty body.
func Decode(r io.Reader) (Body, error) {
	root, err := message.Read(r)
	if err != nil && !isRecoverable(err) {
		return Body{}, fmt.Errorf("rfc5322: parse message: %w", err)
	}

	var b Body
	b.Headers = decodeHeaders(root.Header, &b.Warnings)

	// Walk hands each part's charset or encoding error to the callback
	// together with a usable entity, so one undecodable part does not cost
	// the rest of the message.
	if err := walkWithRootErr(root, err, b.collect); err != nil {
		return Body{}, fmt.Errorf("rfc5322: parse MIME structure: %w", err)
	}
	return b, nil
}

// walkWithRootErr is Entity.Walk with the error message.Read returned for
// the root entity forwarded to the callback, which Walk itself cannot know.
func walkWithRootErr(root *message.Entity, rootErr error, fn message.WalkFunc) error {
	return root.Walk(func(path []int, part *message.Entity, partErr error) error {
		if path == nil && partErr == nil {
			partErr = rootErr
		}
		return fn(path, part, partErr)
	})
}

// collect takes a part's content into b if it is the first inline text/plain
// or text/html part.
func (b *Body) collect(path []int, part *message.Entity, partErr error) error {
	if partErr != nil && !isRecoverable(partErr) {
		return fmt.Errorf("%s: %w", partName(path), partErr)
	}

	mediaType, params, _ := part.Header.ContentType()
	disposition, _, _ := part.Header.ContentDisposition()
	if disposition == "attachment" {
		return nil
	}

	var dst *string
	switch {
	case mediaType == "text/plain" && b.Text == "":
		dst = &b.Text
	case mediaType == "text/html" && b.HTML == "":
		dst = &b.HTML
	default:
		return nil
	}

	content, err := io.ReadAll(part.Body)
	if err != nil {
		// The part's bytes are malformed for its transfer encoding (bad
		// base64, for instance); the message itself still parsed.
		b.Warnings = append(b.Warnings, fmt.Sprintf("%s (%s): content not decoded: %v", partName(path), mediaType, err))
		return nil
	}
	*dst = string(content)

	switch {
	case message.IsUnknownCharset(partErr):
		b.Warnings = append(b.Warnings, fmt.Sprintf("%s (%s): charset %q not supported; text returned undecoded", partName(path), mediaType, params["charset"]))
	case message.IsUnknownEncoding(partErr):
		b.Warnings = append(b.Warnings, fmt.Sprintf("%s (%s): transfer encoding %q not supported; content returned as received", partName(path), mediaType, part.Header.Get("Content-Transfer-Encoding")))
	}
	return nil
}

// decodeHeaders returns the header fields under their wire spelling with
// encoded words decoded. A field whose encoded words cannot be decoded keeps
// its raw value and adds a warning.
func decodeHeaders(h message.Header, warnings *[]string) map[string]string {
	dec := WordDecoder()
	out := make(map[string]string)
	seen := make(map[string]bool)
	fields := h.Fields()
	for fields.Next() {
		canonical := fields.Key()
		if seen[canonical] {
			continue
		}
		seen[canonical] = true

		// Get returns the first occurrence in message order, whatever order
		// Fields iterates in.
		raw := h.Get(canonical)
		value, err := dec.DecodeHeader(raw)
		if err != nil {
			*warnings = append(*warnings, fmt.Sprintf("header %s: %v; value returned undecoded", canonical, err))
			value = raw
		}
		out[wireKey(fields, canonical)] = value
	}
	return out
}

// wireKey returns the field name as the message spells it ("Message-ID"),
// not go-message's canonical form ("Message-Id").
func wireKey(fields message.HeaderFields, canonical string) string {
	raw, err := fields.Raw()
	if err != nil {
		return canonical
	}
	name, _, ok := strings.Cut(string(raw), ":")
	if !ok {
		return canonical
	}
	return strings.TrimSpace(name)
}

// isRecoverable reports whether err leaves the entity readable: go-message
// returns an unknown charset or transfer encoding together with an entity
// whose body is the undecoded bytes.
func isRecoverable(err error) bool {
	return message.IsUnknownCharset(err) || message.IsUnknownEncoding(err)
}

// partName names a part the way IMAP does: "part 1.2", or "message body"
// for a single-part message.
func partName(path []int) string {
	if len(path) == 0 {
		return "message body"
	}
	nums := make([]string, len(path))
	for i, p := range path {
		nums[i] = strconv.Itoa(p + 1)
	}
	return "part " + strings.Join(nums, ".")
}
