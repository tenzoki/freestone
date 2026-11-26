// Package google implements the mail.Provider interface using the Gmail API v1.
package google

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	domain "github.com/kai/freestone/internal/mail"
	"github.com/kai/freestone/internal/mail/rfc5322"
)

// Driver implements mail.Provider for Gmail.
type Driver struct {
	svc *gmail.Service
}

// New creates a Gmail driver using the given token source.
func New(ctx context.Context, ts oauth2.TokenSource) (*Driver, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("gmail: create service: %w", err)
	}
	return &Driver{svc: svc}, nil
}

// Name returns "google".
func (d *Driver) Name() string { return "google" }

// ListMailboxes returns Gmail labels as mailboxes.
func (d *Driver) ListMailboxes(ctx context.Context) ([]domain.Mailbox, error) {
	resp, err := d.svc.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: list labels: %w", err)
	}

	mboxes := make([]domain.Mailbox, 0, len(resp.Labels))
	for _, label := range resp.Labels {
		// Get full label details for message counts.
		full, err := d.svc.Users.Labels.Get("me", label.Id).Context(ctx).Do()
		if err != nil {
			// If we can't get details, use what we have.
			mboxes = append(mboxes, domain.Mailbox{
				Provider: "google",
				ID:       label.Id,
				Name:     label.Name,
			})
			continue
		}
		mboxes = append(mboxes, domain.Mailbox{
			Provider: "google",
			ID:       full.Id,
			Name:     full.Name,
			Messages: uint32(full.MessagesTotal),
			Unseen:   uint32(full.MessagesUnread),
		})
	}
	return mboxes, nil
}

// ListMessages lists messages matching the query criteria.
func (d *Driver) ListMessages(ctx context.Context, q domain.MessageQuery) ([]domain.MessageSummary, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	queryStr := buildGmailQuery(q)
	call := d.svc.Users.Messages.List("me").
		Context(ctx).
		MaxResults(int64(limit))

	if q.MailboxID != "" {
		call = call.LabelIds(q.MailboxID)
	}
	if queryStr != "" {
		call = call.Q(queryStr)
	}

	resp, err := call.Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: list messages: %w", err)
	}

	return d.fetchSummaries(ctx, resp.Messages)
}

// metadataWorkers bounds how many metadata requests ListMessages keeps in
// flight. The list call returns ids only, so a 200-message listing needs 200
// gets; issued one after another they alone can exhaust the tool timeout.
const metadataWorkers = 8

// fetchSummaries fetches the metadata of every listed message with at most
// metadataWorkers requests in flight. The result keeps the list order, which
// is Gmail's newest first. The first failure cancels the requests still
// running and is the error returned: a listing with a hole in it would read
// as complete.
func (d *Driver) fetchSummaries(ctx context.Context, msgs []*gmail.Message) ([]domain.MessageSummary, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	summaries := make([]domain.MessageSummary, len(msgs))
	sem := make(chan struct{}, metadataWorkers)
	var (
		wg       sync.WaitGroup
		failOnce sync.Once
		firstErr error
		// stopped is set when the context ended before every message was
		// dispatched, so some summaries were never fetched.
		stopped bool
	)
	fail := func(err error) {
		failOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}

dispatch:
	for i, msg := range msgs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			stopped = true
			break dispatch
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			full, err := d.svc.Users.Messages.Get("me", msg.Id).
				Context(ctx).
				Format("metadata").
				MetadataHeaders("Subject", "From", "To", "Date").
				Do()
			if err != nil {
				fail(fmt.Errorf("gmail: get message metadata %s: %w", msg.Id, err))
				return
			}
			summaries[i] = messageToSummary(full)
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	// The caller's context ended before every request was dispatched and
	// no request failed, so the context is the reason the listing is short.
	if stopped {
		return nil, fmt.Errorf("gmail: get message metadata: %w", ctx.Err())
	}
	return summaries, nil
}

// GetMessage retrieves the full message content. It fetches the raw RFC 5322
// message rather than Gmail's pre-parsed "full" format, so the body is
// decoded by the same codec as on every other provider: charsets are
// converted, and parts "full" would only reference by attachment id are
// present.
func (d *Driver) GetMessage(ctx context.Context, _, id string) (*domain.Message, error) {
	raw, err := d.svc.Users.Messages.Get("me", id).
		Context(ctx).
		Format("raw").
		Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: get message %s: %w", id, err)
	}

	msg, err := messageFromRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("gmail: %w", err)
	}
	return &msg, nil
}

// SaveDraftReply creates a Gmail draft, threaded on the original message
// when it replies to one: threadId for Gmail's own conversation view, and
// In-Reply-To and References for every other client. Gmail fills in From.
func (d *Driver) SaveDraftReply(ctx context.Context, draft domain.DraftReply) (string, error) {
	composed := rfc5322.Draft{
		To:      draft.To,
		Cc:      draft.Cc,
		Bcc:     draft.Bcc,
		Subject: draft.Subject,
		Text:    draft.BodyText,
		HTML:    draft.BodyHTML,
	}
	gmailMsg := &gmail.Message{}

	if draft.InReplyToID != "" {
		orig, err := d.svc.Users.Messages.Get("me", draft.InReplyToID).
			Context(ctx).
			Format("metadata").
			MetadataHeaders("Message-ID", "References").
			Do()
		if err != nil {
			return "", fmt.Errorf("gmail: get original message %s: %w", draft.InReplyToID, err)
		}
		parentID, parentRefs, err := replyThreading(orig)
		if err != nil {
			return "", fmt.Errorf("gmail: original message %s: %w", draft.InReplyToID, err)
		}
		composed.ParentMessageID = parentID
		composed.ParentReferences = parentRefs
		gmailMsg.ThreadId = orig.ThreadId
	}

	body, err := rfc5322.ComposeDraft(composed)
	if err != nil {
		return "", fmt.Errorf("gmail: compose draft: %w", err)
	}
	gmailMsg.Raw = base64.URLEncoding.EncodeToString(body)

	created, err := d.svc.Users.Drafts.Create("me", &gmail.Draft{
		Message: gmailMsg,
	}).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("gmail: create draft: %w", err)
	}
	if created.Message != nil {
		return created.Message.Id, nil
	}
	return created.Id, nil
}

// MarkRead removes the UNREAD label from a message.
func (d *Driver) MarkRead(ctx context.Context, _, id string) error {
	_, err := d.svc.Users.Messages.Modify("me", id, &gmail.ModifyMessageRequest{
		RemoveLabelIds: []string{"UNREAD"},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("gmail: mark read %s: %w", id, err)
	}
	return nil
}

// buildGmailQuery converts a MessageQuery to a Gmail search query string.
func buildGmailQuery(q domain.MessageQuery) string {
	var parts []string
	if q.From != "" {
		parts = append(parts, fmt.Sprintf("from:%s", q.From))
	}
	if q.Subject != "" {
		parts = append(parts, fmt.Sprintf("subject:%s", q.Subject))
	}
	if !q.Since.IsZero() {
		parts = append(parts, fmt.Sprintf("after:%s", q.Since.Format("2006/01/02")))
	}
	if !q.Before.IsZero() {
		parts = append(parts, fmt.Sprintf("before:%s", q.Before.Format("2006/01/02")))
	}
	if q.Unseen {
		parts = append(parts, "is:unread")
	}
	return strings.Join(parts, " ")
}
