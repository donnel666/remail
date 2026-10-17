package protocol

import (
	"context"
	"strings"
	"time"

	"github.com/donnel666/remail/internal/systemsettings/runtimeconfig"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Validation proves mailbox read access; complete folder scans belong to history.
func (c *Client) Validate(ctx context.Context, credentials Credentials) error {
	ctx, cancel := context.WithTimeout(ctx, runtimeconfig.Duration("imap_operation_timeout_seconds", 60*time.Second, time.Second, 1))
	defer cancel()
	if credentials.Email == "" || credentials.AppPassword == "" || strings.ContainsAny(credentials.Email+credentials.AppPassword, "\r\n\x00") {
		return &Failure{Category: "invalid_credentials", SafeMessage: "Daum APP password is required.", Stage: "credentials"}
	}
	switch credentials.Protocol {
	case "", "imap":
		return c.validateIMAP(ctx, credentials)
	case "pop3":
		return c.validatePOP3(ctx, credentials)
	default:
		return &Failure{Category: "protocol", SafeMessage: "Unsupported Daum mail protocol.", Stage: "credentials"}
	}
}

func (c *Client) validateIMAP(ctx context.Context, credentials Credentials) error {
	conn, closeConn, err := c.connect(ctx, "imap.daum.net:993", credentials.ProxyURL, "imap")
	if err != nil {
		return err
	}
	defer closeConn()
	client := imapclient.New(conn, nil)
	defer client.Close()
	if err := client.WaitGreeting(); err != nil {
		return failureAt(transportFailure(err), "greeting")
	}
	if err := client.Login(credentials.Email, credentials.AppPassword).Wait(); err != nil {
		return failureAt(authFailure(err), "authenticate")
	}
	selected, err := client.Select("INBOX", &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return failureAt(transportFailure(err), "mailbox_access")
	}
	if selected.NumMessages > 0 {
		_, err := client.Fetch(imap.SeqSetNum(selected.NumMessages), &imap.FetchOptions{UID: true, Envelope: true}).Collect()
		if err != nil {
			return failureAt(transportFailure(err), "mailbox_access")
		}
	}
	_ = client.Logout().Wait()
	return nil
}

func (c *Client) validatePOP3(ctx context.Context, credentials Credentials) error {
	session, closeConn, err := c.openPOP3(ctx, credentials)
	if err != nil {
		return err
	}
	defer closeConn()
	if _, err := pop3Command(session, "STAT"); err != nil {
		return failureAt(transportFailure(err), "mailbox_access")
	}
	_, _ = pop3Command(session, "QUIT")
	return nil
}
