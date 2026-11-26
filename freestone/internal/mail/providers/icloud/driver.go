// Package icloud implements the mail.Provider interface using IMAP
// (iCloud Mail).
package icloud

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/kai/freestone/internal/mail"
)

const imapDialTimeout = 30 * time.Second

// Driver is the iCloud IMAP mail provider.
type Driver struct {
	addr          string
	username      string
	password      string
	draftsMailbox string
	// dial opens the connection to the IMAP server. New installs a TLS dial
	// to addr; tests inject a plain TCP dial to an in-process server.
	dial func(ctx context.Context) (net.Conn, error)
}

// New creates a new iCloud mail driver.
func New(host string, port int, username, password, draftsMailbox string) *Driver {
	if draftsMailbox == "" {
		draftsMailbox = "Drafts"
	}
	addr := fmt.Sprintf("%s:%d", host, port)
	return &Driver{
		addr:          addr,
		username:      username,
		password:      password,
		draftsMailbox: draftsMailbox,
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

// connect dials the server, logs in, and returns the client. The caller
// must call Logout when done.
func (d *Driver) connect(ctx context.Context) (*imapclient.Client, error) {
	conn, err := d.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("icloud-mail: dial %s: %w", d.addr, err)
	}

	c := imapclient.New(conn, nil)

	if err := c.Login(d.username, d.password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("icloud-mail: login: %w", err)
	}
	return c, nil
}

// logout ends the session and joins a LOGOUT failure into *errp, so it is
// reported rather than dropped. It must run at function exit: calling
// c.Logout() directly in a defer statement would send LOGOUT before the
// method's own commands.
func logout(c *imapclient.Client, errp *error) {
	if err := c.Logout().Wait(); err != nil {
		*errp = errors.Join(*errp, fmt.Errorf("icloud-mail: logout: %w", err))
	}
}

func (d *Driver) ListMailboxes(ctx context.Context) (_ []mail.Mailbox, err error) {
	c, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { logout(c, &err) }()

	listCmd := c.List("", "*", &imap.ListOptions{
		ReturnStatus: &imap.StatusOptions{
			NumMessages: true,
			NumUnseen:   true,
		},
	})

	var result []mail.Mailbox
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
		return nil, fmt.Errorf("icloud-mail: list: %w", err)
	}

	return result, nil
}

func (d *Driver) ListMessages(ctx context.Context, q mail.MessageQuery) (_ []mail.MessageSummary, err error) {
	c, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { logout(c, &err) }()

	mboxID := q.MailboxID
	if mboxID == "" {
		mboxID = "INBOX"
	}

	if _, err := c.Select(mboxID, nil).Wait(); err != nil {
		return nil, fmt.Errorf("icloud-mail: select %s: %w", mboxID, err)
	}

	criteria := buildSearchCriteria(q)
	searchData, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("icloud-mail: search: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
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

	var result []mail.MessageSummary
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
		return nil, fmt.Errorf("icloud-mail: fetch: %w", err)
	}
	return result, nil
}

func (d *Driver) GetMessage(ctx context.Context, mailboxID, id string) (_ *mail.Message, err error) {
	c, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { logout(c, &err) }()

	if mailboxID == "" {
		mailboxID = "INBOX"
	}
	if _, err := c.Select(mailboxID, nil).Wait(); err != nil {
		return nil, fmt.Errorf("icloud-mail: select %s: %w", mailboxID, err)
	}

	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("icloud-mail: invalid message ID %q: %w", id, err)
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
		fetchCmd.Close()
		return nil, fmt.Errorf("icloud-mail: message %s not found", id)
	}

	msg := &mail.Message{
		MessageSummary: mail.MessageSummary{
			Provider:  "icloud",
			MailboxID: mailboxID,
			ID:        id,
		},
		Headers: make(map[string]string),
	}

	for {
		item := msgData.Next()
		if item == nil {
			break
		}
		switch data := item.(type) {
		case imapclient.FetchItemDataEnvelope:
			msg.Subject = data.Envelope.Subject
			if len(data.Envelope.From) > 0 {
				msg.From = formatAddress(data.Envelope.From[0])
			}
			for _, to := range data.Envelope.To {
				msg.To = append(msg.To, formatAddress(to))
			}
		case imapclient.FetchItemDataFlags:
			for _, f := range data.Flags {
				msg.Flags = append(msg.Flags, string(f))
			}
		case imapclient.FetchItemDataInternalDate:
			msg.Date = data.Time
		case imapclient.FetchItemDataRFC822Size:
			msg.Size = uint32(data.Size)
		case imapclient.FetchItemDataBodySection:
			text, html := decodeBody(data.Literal)
			msg.Text = text
			msg.HTML = html
		}
	}

	return msg, fetchCmd.Close()
}

func (d *Driver) SaveDraftReply(ctx context.Context, draft mail.DraftReply) (_ string, err error) {
	c, err := d.connect(ctx)
	if err != nil {
		return "", err
	}
	defer func() { logout(c, &err) }()

	// If replying, fetch the original message's Message-ID for threading.
	var inReplyToMsgID string
	if draft.InReplyToID != "" {
		mailboxID := draft.InReplyToMailboxID
		if mailboxID == "" {
			mailboxID = "INBOX"
		}
		if _, err := c.Select(mailboxID, nil).Wait(); err != nil {
			return "", fmt.Errorf("icloud-mail: select %s for reply: %w", mailboxID, err)
		}
		origUID, parseErr := strconv.ParseUint(draft.InReplyToID, 10, 32)
		if parseErr == nil {
			uidSet := imap.UIDSetNum(imap.UID(origUID))
			fetchOpts := &imap.FetchOptions{Envelope: true}
			fetchCmd := c.Fetch(uidSet, fetchOpts)
			if msgData := fetchCmd.Next(); msgData != nil {
				for {
					item := msgData.Next()
					if item == nil {
						break
					}
					if envData, ok := item.(imapclient.FetchItemDataEnvelope); ok {
						if envData.Envelope != nil {
							inReplyToMsgID = envData.Envelope.MessageID
						}
					}
				}
			}
			fetchCmd.Close()
		}
	}

	body, err := encodeDraftReply(draft, inReplyToMsgID)
	if err != nil {
		return "", fmt.Errorf("icloud-mail: encode draft: %w", err)
	}

	appendCmd := c.Append(d.draftsMailbox, int64(len(body)), nil)
	if _, err := appendCmd.Write(body); err != nil {
		return "", fmt.Errorf("icloud-mail: append write: %w", err)
	}
	if err := appendCmd.Close(); err != nil {
		return "", fmt.Errorf("icloud-mail: append close: %w", err)
	}
	data, err := appendCmd.Wait()
	if err != nil {
		return "", fmt.Errorf("icloud-mail: append: %w", err)
	}

	// Return the new UID if the server provides it (UIDPLUS extension).
	if data != nil && data.UID != 0 {
		return strconv.FormatUint(uint64(data.UID), 10), nil
	}
	return "", nil
}

func (d *Driver) MarkRead(ctx context.Context, mailboxID, id string) (err error) {
	c, err := d.connect(ctx)
	if err != nil {
		return err
	}
	defer func() { logout(c, &err) }()

	if mailboxID == "" {
		mailboxID = "INBOX"
	}
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
