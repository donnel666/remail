package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/infra/msacl"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

type forwardingMailbox struct {
	local              msacl.MailboxReader
	username, password string
	mu                 sync.Mutex
	conn               net.Conn
	client             *imapclient.Client
	refreshed          time.Time
	messages           []msacl.EmailObj
}

func (r *forwardingMailbox) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		_ = r.client.Close()
		r.client = nil
	}
}

func (r *forwardingMailbox) List(ctx context.Context, address string, limit int, fuzzy bool) ([]msacl.EmailObj, error) {
	if !strings.HasSuffix(strings.ToLower(address), "@fan7z.com") {
		return r.local.List(ctx, address, limit, fuzzy)
	}
	if fuzzy {
		return nil, errors.New("forwarded mailbox lookup requires an exact address")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if time.Since(r.refreshed) > 2*time.Second {
		if err := r.refresh(ctx); err != nil {
			if r.client != nil {
				_ = r.client.Close()
				r.client = nil
			}
			return nil, err
		}
	}
	var result []msacl.EmailObj
	for i := len(r.messages) - 1; i >= 0; i-- {
		if strings.EqualFold(r.messages[i].To, address) {
			result = append(result, r.messages[i])
			if limit > 0 && len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (r *forwardingMailbox) refresh(ctx context.Context) error {
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	needsLogin := r.client == nil
	if needsLogin {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: &tls.Config{ServerName: "imap.qq.com", MinVersion: tls.VersionTLS12}}
		conn, err := dialer.DialContext(ctx, "tcp", "imap.qq.com:993")
		if err != nil {
			return errors.New("QQ IMAP connection failed")
		}
		r.conn = conn
		r.client = imapclient.New(conn, nil)
	}
	conn := r.conn
	// The IMAP client replaces socket deadlines; cancellation must close the connection.
	stopClose := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopClose()
	_ = conn.SetDeadline(deadline)
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	if needsLogin {
		if err := r.client.Login(r.username, r.password).Wait(); err != nil {
			return errors.New("QQ IMAP login failed")
		}
	}
	selected, err := r.client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return errors.New("QQ IMAP inbox selection failed")
	}
	r.messages = nil
	if selected.NumMessages > 0 {
		// shortcut: inspect the newest 50 messages per poll; use UID checkpoints if traffic can exceed 50 messages in two seconds.
		start := uint32(1)
		if selected.NumMessages > 50 {
			start = selected.NumMessages - 49
		}
		set := imap.SeqSet{}
		set.AddRange(start, selected.NumMessages)
		section := &imap.FetchItemBodySection{Peek: true}
		fetch := r.client.Fetch(set, &imap.FetchOptions{UID: true, InternalDate: true, BodySection: []*imap.FetchItemBodySection{section}})
		for message := fetch.Next(); message != nil; message = fetch.Next() {
			row, err := message.Collect()
			if err != nil {
				_ = fetch.Close()
				return errors.New("QQ IMAP message fetch failed")
			}
			raw := row.FindBodySection(section)
			if len(raw) == 0 || len(raw) > 2<<20 {
				continue
			}
			if item, ok := parseForwardedCode(raw, fmt.Sprintf("qq:%d:%d", selected.UIDValidity, row.UID), row.InternalDate); ok {
				r.messages = append(r.messages, item)
			}
		}
		if err := fetch.Close(); err != nil {
			return errors.New("QQ IMAP fetch did not finish")
		}
	}
	r.refreshed = time.Now()
	return nil
}

func parseForwardedCode(raw []byte, id string, received time.Time) (msacl.EmailObj, bool) {
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return msacl.EmailObj{}, false
	}
	sender, err := mail.ParseAddress(message.Header.Get("From"))
	if err != nil || !strings.EqualFold(sender.Address, "account-security-noreply@accountprotection.microsoft.com") {
		return msacl.EmailObj{}, false
	}
	var target string
	for _, key := range []string{"To", "X-Original-To"} {
		addresses, _ := mail.ParseAddressList(message.Header.Get(key))
		for _, address := range addresses {
			if strings.HasSuffix(strings.ToLower(address.Address), "@fan7z.com") {
				if target != "" && !strings.EqualFold(target, address.Address) {
					return msacl.EmailObj{}, false
				}
				target = strings.ToLower(address.Address)
			}
		}
	}
	if target == "" {
		return msacl.EmailObj{}, false
	}
	summary := mailapp.ParseInboundMessageSummary(raw, received)
	return msacl.EmailObj{ID: id, To: target, From: sender.Address, Subject: summary.Subject, Preview: summary.BodyPreview, VerificationCode: summary.VerificationCode, ReceivedAt: summary.ReceivedAt.Format(time.RFC3339Nano)}, true
}
