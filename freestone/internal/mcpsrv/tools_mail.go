package mcpsrv

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kai/freestone/internal/mail"
)

type listMailboxesInput struct {
	Provider string `json:"provider,omitempty" jsonschema:"Provider name. Omit to list all."`
}

type listMessagesInput struct {
	Provider  string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	MailboxID string `json:"mailbox_id" jsonschema:"Mailbox ID from list_mailboxes"`
	Since     string `json:"since,omitempty" jsonschema:"Only messages after this RFC3339 date"`
	Before    string `json:"before,omitempty" jsonschema:"Only messages before this RFC3339 date"`
	Unseen    bool   `json:"unseen,omitempty" jsonschema:"Only unread messages"`
	From      string `json:"from,omitempty" jsonschema:"Filter by sender address"`
	Subject   string `json:"subject,omitempty" jsonschema:"Filter by subject text"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Max results (default 50, cap 200)"`
}

type getMessageInput struct {
	Provider  string `json:"provider" jsonschema:"Provider name (icloud or google)"`
	MailboxID string `json:"mailbox_id" jsonschema:"Mailbox ID"`
	ID        string `json:"id" jsonschema:"Message ID"`
}

func registerMailTools(srv *mcp.Server, reg *mail.Registry, log *slog.Logger) {
	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "mail_list_mailboxes",
			Description: "List mailboxes/folders. Omit provider to list from all enabled providers.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listMailboxesInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			if input.Provider == "" {
				mboxes, err := reg.ListAllMailboxes(ctx)
				if err != nil {
					return errResult(err), nil, nil
				}
				return jsonResult(mboxes), nil, nil
			}
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			mboxes, err := p.ListMailboxes(ctx)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(mboxes), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "mail_list_messages",
			Description: "List messages in a mailbox with optional filters.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input listMessagesInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			q := mail.MessageQuery{
				MailboxID: input.MailboxID,
				Unseen:    input.Unseen,
				From:      input.From,
				Subject:   input.Subject,
				Limit:     input.Limit,
			}
			if input.Since != "" {
				q.Since, err = time.Parse(time.RFC3339, input.Since)
				if err != nil {
					return errResult(fmt.Errorf("invalid since: %w", err)), nil, nil
				}
			}
			if input.Before != "" {
				q.Before, err = time.Parse(time.RFC3339, input.Before)
				if err != nil {
					return errResult(fmt.Errorf("invalid before: %w", err)), nil, nil
				}
			}
			msgs, err := p.ListMessages(ctx, q)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(msgs), nil, nil
		},
	)

	mcp.AddTool(srv,
		&mcp.Tool{
			Name:        "mail_get_message",
			Description: "Get full message content including body.",
		},
		func(ctx context.Context, _ *mcp.CallToolRequest, input getMessageInput) (*mcp.CallToolResult, any, error) {
			ctx, cancel := withTimeout(ctx)
			defer cancel()
			p, err := reg.Provider(input.Provider)
			if err != nil {
				return errResult(err), nil, nil
			}
			msg, err := p.GetMessage(ctx, input.MailboxID, input.ID)
			if err != nil {
				return errResult(err), nil, nil
			}
			return jsonResult(msg), nil, nil
		},
	)
}
