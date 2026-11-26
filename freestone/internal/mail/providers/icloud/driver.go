// Package icloud implements the mail.Provider interface using IMAP
// (iCloud Mail).
package icloud

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	netmail "net/mail"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/kai/freestone/internal/mail"
	"github.com/kai/freestone/internal/mail/rfc5322"
)

const imapDialTimeout = 30 * time.Second

// Driver is the iCloud IMAP mail provider.
type Driver struct {
	addr          string
	username      string
	password      string
	draftsMailbox string
	// fromAddress is the configured sender for drafts; empty falls back to
	// username (see draftFrom).
	fromAddress string
	// dial opens the connection to the IMAP server. New installs a TLS dial
	// to addr; tests inject a plain TCP dial to an in-process server.
	dial func(ctx context.Context) (net.Conn, error)
	// log records session cleanup failures (LOGOUT, close) that must stay
	// visible but must not turn a completed operation into an error.
	log *slog.Logger
}

// New creates a new iCloud mail driver.
// fromAddress is the sender written into drafts; empty uses username.
func New(host string, port int, username, password, draftsMailbox, fromAddress string, log *slog.Logger) *Driver {
	if draftsMailbox == "" {
		draftsMailbox = "Drafts"
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	return &Driver{
		addr:          addr,
		username:      username,
		password:      password,
		draftsMailbox: draftsMailbox,
		fromAddress:   fromAddress,
		log:           log,
		dial: func(ctx context.Context) (net.Conn, error) {
			return dialTLS(ctx, addr)
		},
	}
}

func (d *Driver) Name() string { return "icloud" }

// dialTLS opens a TLS connection to addr with a timeout derived from ctx.
func dialTLS(ctx context.Context, addr string) (net.Conn, error) {
	// Derive deadline from context, fall back to imapDialTimeout.
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(imapDialTimeout)
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{
			Timeout:  time.Until(deadline),
			Deadline: deadline,
		},
	}
	return dialer.DialContext(ctx, "tcp", addr)
}

// session runs fn on a fresh, logged-in connection and ends the session.
//
// go-imap v2 takes no context, and connection deadlines cannot carry one:
// the library resets the read deadline for every response. So cancellation
// reaches the connection by closing it: an AfterFunc registered right after
// the dial, before LOGIN, so that a stalled login is cancellable as well.
// Closing makes the blocked command return, and the returned error then
// wraps ctx.Err(), so a timeout reads as a timeout rather than as a closed
// connection.
//
// The result is fn's: a LOGOUT or close failure after fn has returned is
// logged, never returned. A completed write such as an APPEND must not be
// reported as failed, or a caller that retries on error writes it twice.
func (d *Driver) session(ctx context.Context, op string, fn func(c *imapclient.Client) error) error {
	conn, err := d.dial(ctx)
	if err != nil {
		return fmt.Errorf("icloud-mail: dial %s: %w", d.addr, err)
	}

	// The word decoder makes envelope subjects and address names in
	// ISO-8859-1, Windows-1252 and other charsets arrive decoded; the
	// library's default decodes UTF-8 and US-ASCII only.
	c := imapclient.New(conn, &imapclient.Options{WordDecoder: rfc5322.WordDecoder()})
	stop := context.AfterFunc(ctx, func() {
		// A close error here is the same one the cleanup below sees and logs.
		_ = c.Close()
	})

	opErr := c.Login(d.username, d.password).Wait()
	if opErr != nil {
		opErr = fmt.Errorf("icloud-mail: login: %w", opErr)
	} else {
		opErr = fn(c)
	}

	// stop reports false once the AfterFunc has started: the context is done
	// and the connection is being closed, so LOGOUT cannot be sent.
	cancelled := !stop()
	d.endSession(c, op, cancelled, opErr == nil)

	if opErr != nil && ctx.Err() != nil {
		return fmt.Errorf("icloud-mail: %s: %w: %w", op, ctx.Err(), opErr)
	}
	return opErr
}

// endSession logs out when the session is still usable and closes the
// connection. Failures are logged: they cannot change the operation's outcome.
func (d *Driver) endSession(c *imapclient.Client, op string, cancelled, opSucceeded bool) {
	if !cancelled && c.State() != imap.ConnStateLogout {
		if err := c.Logout().Wait(); err != nil {
			d.log.Warn("icloud-mail: logout failed", "op", op, "op_succeeded", opSucceeded, "err", err)
		}
	}
	// After a cancellation the AfterFunc has already closed the client, and
	// a second Close reports net.ErrClosed by design.
	if err := c.Close(); err != nil && !(cancelled && errors.Is(err, net.ErrClosed)) {
		d.log.Warn("icloud-mail: close connection failed", "op", op, "op_succeeded", opSucceeded, "err", err)
	}
}

func (d *Driver) ListMailboxes(ctx context.Context) ([]mail.Mailbox, error) {
	var result []mail.Mailbox
	err := d.session(ctx, "list mailboxes", func(c *imapclient.Client) error {
		listCmd := c.List("", "*", &imap.ListOptions{
			ReturnStatus: &imap.StatusOptions{
				NumMessages: true,
				NumUnseen:   true,
			},
		})

		for {
			mbox := listCmd.Next()
			if mbox == nil {
				break
			}

			mb := mail.Mailbox{
				Provider:  "icloud",
				ID:        mbox.Mailbox,
				Name:      mbox.Mailbox,
				Delimiter: string(mbox.Delim),
			}
			for _, a := range mbox.Attrs {
				mb.Attributes = append(mb.Attributes, string(a))
			}
			if mbox.Status != nil {
				if mbox.Status.NumMessages != nil {
					mb.Messages = *mbox.Status.NumMessages
				}
				if mbox.Status.NumUnseen != nil {
					mb.Unseen = *mbox.Status.NumUnseen
				}
			}
			result = append(result, mb)
		}

		if err := listCmd.Close(); err != nil {
			return fmt.Errorf("icloud-mail: list: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (d *Driver) ListMessages(ctx context.Context, q mail.MessageQuery) ([]mail.MessageSummary, error) {
	mboxID := q.MailboxID
	if mboxID == "" {
		mboxID = "INBOX"
	}

	var result []mail.MessageSummary
	err := d.session(ctx, "list messages", func(c *imapclient.Client) error {
		if _, err := c.Select(mboxID, nil).Wait(); err != nil {
			return fmt.Errorf("icloud-mail: select %s: %w", mboxID, err)
		}

		criteria := buildSearchCriteria(q)
		searchData, err := c.UIDSearch(criteria, nil).Wait()
		if err != nil {
			return fmt.Errorf("icloud-mail: search: %w", err)
		}

		uids := searchData.AllUIDs()
		if len(uids) == 0 {
			return nil
		}

		limit := q.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200
		}
		if len(uids) > limit {
			uids = uids[len(uids)-limit:]
		}

		uidSet := imap.UIDSetNum(uids...)
		fetchOptions := &imap.FetchOptions{
			Envelope:     true,
			Flags:        true,
			UID:          true,
			InternalDate: true,
			RFC822Size:   true,
		}

		fetchCmd := c.Fetch(uidSet, fetchOptions)

		for {
			msgData := fetchCmd.Next()
			if msgData == nil {
				break
			}

			var buf imapclient.FetchMessageBuffer
			// Consume all items from the streaming response.
			for {
				item := msgData.Next()
				if item == nil {
					break
				}
				switch data := item.(type) {
				case imapclient.FetchItemDataUID:
					buf.UID = imap.UID(data.UID)
				case imapclient.FetchItemDataFlags:
					buf.Flags = data.Flags
				case imapclient.FetchItemDataEnvelope:
					buf.Envelope = data.Envelope
				case imapclient.FetchItemDataInternalDate:
					buf.InternalDate = data.Time
				case imapclient.FetchItemDataRFC822Size:
					buf.RFC822Size = data.Size
				}
			}

			ms := mail.MessageSummary{
				Provider:  "icloud",
				MailboxID: mboxID,
				ID:        strconv.FormatUint(uint64(buf.UID), 10),
				Date:      buf.InternalDate,
				Size:      uint32(buf.RFC822Size),
			}
			if buf.Envelope != nil {
				ms.Subject = buf.Envelope.Subject
				if len(buf.Envelope.From) > 0 {
					ms.From = formatAddress(buf.Envelope.From[0])
				}
				for _, to := range buf.Envelope.To {
					ms.To = append(ms.To, formatAddress(to))
				}
			}
			for _, f := range buf.Flags {
				ms.Flags = append(ms.Flags, string(f))
			}
			result = append(result, ms)
		}

		if err := fetchCmd.Close(); err != nil {
			return fmt.Errorf("icloud-mail: fetch: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (d *Driver) GetMessage(ctx context.Context, mailboxID, id string) (*mail.Message, error) {
	if mailboxID == "" {
		mailboxID = "INBOX"
	}

	var msg *mail.Message
	err := d.session(ctx, "get message", func(c *imapclient.Client) error {
		if _, err := c.Select(mailboxID, nil).Wait(); err != nil {
			return fmt.Errorf("icloud-mail: select %s: %w", mailboxID, err)
		}

		uid, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return fmt.Errorf("icloud-mail: invalid message ID %q: %w", id, err)
		}

		uidSet := imap.UIDSetNum(imap.UID(uid))
		// Peek: reading a message must not mark it read; only MarkRead sets \Seen.
		bodySection := &imap.FetchItemBodySection{Peek: true}
		fetchOptions := &imap.FetchOptions{
			Envelope:     true,
			Flags:        true,
			UID:          true,
			InternalDate: true,
			RFC822Size:   true,
			BodySection:  []*imap.FetchItemBodySection{bodySection},
		}

		fetchCmd := c.Fetch(uidSet, fetchOptions)

		msgData := fetchCmd.Next()
		if msgData == nil {
			if err := fetchCmd.Close(); err != nil {
				return fmt.Errorf("icloud-mail: fetch %s: %w", id, err)
			}
			return fmt.Errorf("icloud-mail: message %s not found", id)
		}

		m := &mail.Message{
			MessageSummary: mail.MessageSummary{
				Provider:  "icloud",
				MailboxID: mailboxID,
				ID:        id,
			},
		}

		var gotBody bool
		var decodeErr error
		for {
			item := msgData.Next()
			if item == nil {
				break
			}
			switch data := item.(type) {
			case imapclient.FetchItemDataEnvelope:
				m.Subject = data.Envelope.Subject
				if len(data.Envelope.From) > 0 {
					m.From = formatAddress(data.Envelope.From[0])
				}
				for _, to := range data.Envelope.To {
					m.To = append(m.To, formatAddress(to))
				}
			case imapclient.FetchItemDataFlags:
				for _, f := range data.Flags {
					m.Flags = append(m.Flags, string(f))
				}
			case imapclient.FetchItemDataInternalDate:
				m.Date = data.Time
			case imapclient.FetchItemDataRFC822Size:
				m.Size = uint32(data.Size)
			case imapclient.FetchItemDataBodySection:
				if data.Literal == nil {
					continue
				}
				gotBody = true
				body, err := rfc5322.Decode(data.Literal)
				if err != nil {
					decodeErr = fmt.Errorf("icloud-mail: decode message %s: %w", id, err)
					continue
				}
				m.Headers = body.Headers
				m.Text = body.Text
				m.HTML = body.HTML
				m.Warnings = body.Warnings
			}
		}

		// Close drains whatever a failed decode left unread.
		if err := fetchCmd.Close(); err != nil {
			return fmt.Errorf("icloud-mail: fetch %s: %w", id, err)
		}
		if decodeErr != nil {
			return decodeErr
		}
		if !gotBody {
			return fmt.Errorf("icloud-mail: message %s: server returned no body", id)
		}
		msg = m
		return nil
	})
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func (d *Driver) SaveDraftReply(ctx context.Context, draft mail.DraftReply) (string, error) {
	from, err := d.draftFrom()
	if err != nil {
		return "", err
	}

	var draftID string
	err = d.session(ctx, "save draft reply", func(c *imapclient.Client) error {
		composed := rfc5322.Draft{
			From:    from,
			To:      draft.To,
			Cc:      draft.Cc,
			Bcc:     draft.Bcc,
			Subject: draft.Subject,
			Text:    draft.BodyText,
			HTML:    draft.BodyHTML,
		}
		if draft.InReplyToID != "" {
			parentID, parentRefs, err := d.replyParent(c, draft.InReplyToMailboxID, draft.InReplyToID)
			if err != nil {
				return err
			}
			composed.ParentMessageID = parentID
			composed.ParentReferences = parentRefs
		}

		body, err := rfc5322.ComposeDraft(composed)
		if err != nil {
			return fmt.Errorf("icloud-mail: compose draft: %w", err)
		}

		// \Draft marks the message as a draft for every client reading the
		// mailbox; without it Mail.app shows it as a received message.
		appendCmd := c.Append(d.draftsMailbox, int64(len(body)), &imap.AppendOptions{Flags: []imap.Flag{imap.FlagDraft}})
		if _, err := appendCmd.Write(body); err != nil {
			return fmt.Errorf("icloud-mail: append write: %w", err)
		}
		if err := appendCmd.Close(); err != nil {
			return fmt.Errorf("icloud-mail: append close: %w", err)
		}
		data, err := appendCmd.Wait()
		if err != nil {
			return fmt.Errorf("icloud-mail: append: %w", err)
		}

		// Return the new UID if the server provides it (UIDPLUS extension).
		if data != nil && data.UID != 0 {
			draftID = strconv.FormatUint(uint64(data.UID), 10)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return draftID, nil
}

// draftFrom is the From address of a draft: from_address when configured,
// else the login name when it is an address. iCloud logins can be an Apple
// ID that is not a mail address, and a draft without a sender is not one a
// client can send.
func (d *Driver) draftFrom() (string, error) {
	if d.fromAddress != "" {
		return d.fromAddress, nil
	}
	if _, err := netmail.ParseAddress(d.username); err != nil {
		return "", fmt.Errorf("icloud-mail: no sender for drafts: set mail.icloud.from_address (username %q is not a mail address: %v)", d.username, err)
	}
	return d.username, nil
}

// replyParent returns the Message-ID and References chain of the message
// being replied to. A reply whose parent cannot be read is an error: saving
// it unthreaded would look like success and break the conversation.
func (d *Driver) replyParent(c *imapclient.Client, mailboxID, id string) (string, []string, error) {
	if mailboxID == "" {
		mailboxID = "INBOX"
	}
	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return "", nil, fmt.Errorf("icloud-mail: invalid reply message ID %q: %w", id, err)
	}
	if _, err := c.Select(mailboxID, nil).Wait(); err != nil {
		return "", nil, fmt.Errorf("icloud-mail: select %s for reply: %w", mailboxID, err)
	}

	// Peek: looking up the parent must not mark it read.
	refsSection := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"References"},
		Peek:         true,
	}
	bufs, err := c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID:         true,
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{refsSection},
	}).Collect()
	if err != nil {
		return "", nil, fmt.Errorf("icloud-mail: fetch reply parent %s: %w", id, err)
	}
	if len(bufs) == 0 {
		return "", nil, fmt.Errorf("icloud-mail: reply parent %s not found in %s", id, mailboxID)
	}
	parent := bufs[0]
	if parent.Envelope == nil || parent.Envelope.MessageID == "" {
		return "", nil, fmt.Errorf("icloud-mail: reply parent %s has no Message-ID to thread on", id)
	}
	refs, err := rfc5322.HeaderReferences(parent.FindBodySection(refsSection))
	if err != nil {
		return "", nil, fmt.Errorf("icloud-mail: reply parent %s: %w", id, err)
	}
	return parent.Envelope.MessageID, refs, nil
}

func (d *Driver) MarkRead(ctx context.Context, mailboxID, id string) error {
	if mailboxID == "" {
		mailboxID = "INBOX"
	}

	return d.session(ctx, "mark read", func(c *imapclient.Client) error {
		if _, err := c.Select(mailboxID, nil).Wait(); err != nil {
			return fmt.Errorf("icloud-mail: select %s: %w", mailboxID, err)
		}

		uid, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return fmt.Errorf("icloud-mail: invalid message ID %q: %w", id, err)
		}

		uidSet := imap.UIDSetNum(imap.UID(uid))
		storeFlags := &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagSeen},
		}
		storeCmd := c.Store(uidSet, storeFlags, nil)
		// Drain the command response.
		for {
			if storeCmd.Next() == nil {
				break
			}
		}
		if err := storeCmd.Close(); err != nil {
			return fmt.Errorf("icloud-mail: mark read %s: %w", id, err)
		}
		return nil
	})
}

func buildSearchCriteria(q mail.MessageQuery) *imap.SearchCriteria {
	criteria := &imap.SearchCriteria{}
	if !q.Since.IsZero() {
		criteria.Since = q.Since
	}
	if !q.Before.IsZero() {
		criteria.Before = q.Before
	}
	if q.Unseen {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	if q.From != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
			Key:   "From",
			Value: q.From,
		})
	}
	if q.Subject != "" {
		criteria.Header = append(criteria.Header, imap.SearchCriteriaHeaderField{
			Key:   "Subject",
			Value: q.Subject,
		})
	}
	return criteria
}

func formatAddress(addr imap.Address) string {
	if addr.Name != "" {
		return fmt.Sprintf("%s <%s@%s>", addr.Name, addr.Mailbox, addr.Host)
	}
	return fmt.Sprintf("%s@%s", addr.Mailbox, addr.Host)
}
