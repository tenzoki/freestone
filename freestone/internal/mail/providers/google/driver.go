// Package google implements the mail.Provider interface using the Gmail API v1.
package google

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	domain "github.com/kai/freestone/internal/mail"
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

	summaries := make([]domain.MessageSummary, 0, len(resp.Messages))
	for _, msg := range resp.Messages {
		full, err := d.svc.Users.Messages.Get("me", msg.Id).
			Context(ctx).
			Format("metadata").
			MetadataHeaders("Subject", "From", "To", "Date").
			Do()
		if err != nil {
			return nil, fmt.Errorf("gmail: get message metadata %s: %w", msg.Id, err)
		}
		summary := messageToSummary(full)
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

// GetMessage retrieves the full message content.
func (d *Driver) GetMessage(ctx context.Context, _, id string) (*domain.Message, error) {
	full, err := d.svc.Users.Messages.Get("me", id).
		Context(ctx).
		Format("full").
		Do()
	if err != nil {
		return nil, fmt.Errorf("gmail: get message %s: %w", id, err)
	}

	msg := messageFromFull(full)
	return &msg, nil
}

// SaveDraftReply creates a Gmail draft as a reply to an existing message.
// It fetches the original message's threadId for threading, encodes a
// minimal RFC 5322 message, and calls Drafts.Create.
func (d *Driver) SaveDraftReply(ctx context.Context, draft domain.DraftReply) (string, error) {
	var threadID string

	// Fetch the original message's ThreadId for proper threading.
	if draft.InReplyToID != "" {
		orig, err := d.svc.Users.Messages.Get("me", draft.InReplyToID).
			Context(ctx).
			Format("metadata").
			MetadataHeaders("Message-ID").
			Do()
		if err != nil {
			return "", fmt.Errorf("gmail: get original message %s: %w", draft.InReplyToID, err)
		}
		threadID = orig.ThreadId
	}

	body := buildRFC5322(draft)
	raw := base64.URLEncoding.EncodeToString(body)

	gmailMsg := &gmail.Message{Raw: raw}
	if threadID != "" {
		gmailMsg.ThreadId = threadID
	}

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
