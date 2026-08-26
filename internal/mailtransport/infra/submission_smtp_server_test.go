package infra

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	settingsdomain "github.com/donnel666/remail/internal/systemsettings/domain"
	"github.com/emersion/go-sasl"
	smtpserver "github.com/emersion/go-smtp"
	"github.com/stretchr/testify/require"
)

type submissionAuthenticatorFunc func(context.Context, string) (uint, error)

func (f submissionAuthenticatorFunc) AuthenticateSMTPSubmissionKey(ctx context.Context, plain string) (uint, error) {
	return f(ctx, plain)
}

type submissionDeliverySpy struct {
	messages []domain.OutboundMessage
}

func (s *submissionDeliverySpy) Send(_ context.Context, message domain.OutboundMessage) error {
	message.RawMessage = append([]byte(nil), message.RawMessage...)
	s.messages = append(s.messages, message)
	return nil
}

func TestSMTPSubmissionAuthenticatesTokenSanitizesHeadersAndUsesUniqueTransactions(t *testing.T) {
	delivery := &submissionDeliverySpy{}
	server := NewSubmissionSMTPServer(SubmissionSMTPConfig{}, nil, delivery)
	require.Equal(t, 1, server.server.MaxRecipients)
	session := &submissionSMTPSession{
		authenticator: submissionAuthenticatorFunc(func(_ context.Context, plain string) (uint, error) {
			require.Equal(t, "sk_secret", plain)
			return 7, nil
		}),
		delivery:        delivery,
		maxMessageBytes: 1024,
	}
	require.ErrorIs(t, session.Mail("sender@example.com", nil), smtpserver.ErrAuthRequired)

	auth, err := session.Auth(sasl.Plain)
	require.NoError(t, err)
	_, done, err := auth.Next([]byte("\x00any-username\x00sk_secret"))
	require.NoError(t, err)
	require.True(t, done)

	require.NoError(t, session.Mail("sender@example.com", nil))
	require.NoError(t, session.Rcpt("first@example.net", nil))
	raw := "Return-Path: <forged@example.org>\r\nFrom: App <sender@example.com>\r\nBcc: hidden@example.net\r\nResent-Bcc: hidden-again@example.net\r\nSubject: test\r\nX-App: local\r\nContent-Type: text/plain\r\n\r\nhello"
	wantRaw := "From: App <sender@example.com>\r\nSubject: test\r\nX-App: local\r\nContent-Type: text/plain\r\n\r\nhello"
	require.NoError(t, session.Data(strings.NewReader(raw)))
	require.Len(t, delivery.messages, 1)
	message := delivery.messages[0]
	require.Equal(t, domain.PurposeSMTPSubmission, message.Purpose)
	require.Equal(t, "sender@example.com", message.From)
	require.Equal(t, "first@example.net", message.To)
	require.Equal(t, wantRaw, string(message.RawMessage))
	require.NotEmpty(t, message.IdempotencyKey)

	session.Reset()
	require.NoError(t, session.Mail("sender@example.com", nil))
	require.NoError(t, session.Rcpt("first@example.net", nil))
	require.NoError(t, session.Data(strings.NewReader(raw)))
	require.Len(t, delivery.messages, 2)
	require.NotEqual(t, delivery.messages[0].IdempotencyKey, delivery.messages[1].IdempotencyKey)
	require.Equal(t, "first@example.net", delivery.messages[0].To)
}

func TestSMTPSubmissionRejectsInvalidToken(t *testing.T) {
	session := &submissionSMTPSession{authenticator: submissionAuthenticatorFunc(func(context.Context, string) (uint, error) {
		return 0, settingsdomain.ErrInvalidSystemKey
	})}
	auth, err := session.Auth(sasl.Plain)
	require.NoError(t, err)
	_, done, err := auth.Next([]byte("\x00ignored\x00wrong"))
	require.True(t, done)
	require.True(t, errors.Is(err, smtpserver.ErrAuthFailed))
	require.False(t, session.authenticated)
}

func TestSMTPSubmissionSupportsCommonAuthLoginClients(t *testing.T) {
	session := &submissionSMTPSession{authenticator: submissionAuthenticatorFunc(func(_ context.Context, plain string) (uint, error) {
		require.Equal(t, "sk_login", plain)
		return 1, nil
	})}
	client := sasl.NewLoginClient("ignored", "sk_login")
	mechanism, initial, err := client.Start()
	require.NoError(t, err)
	auth, err := session.Auth(mechanism)
	require.NoError(t, err)
	challenge, done, err := auth.Next(initial)
	require.NoError(t, err)
	require.False(t, done)
	response, err := client.Next(challenge)
	require.NoError(t, err)
	_, done, err = auth.Next(response)
	require.NoError(t, err)
	require.True(t, done)
	require.True(t, session.authenticated)
}

type submissionDataErrorReader struct {
	err error
}

func (r submissionDataErrorReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestSMTPSubmissionPreservesMessageTooLargeStatus(t *testing.T) {
	session := &submissionSMTPSession{
		authenticated:   true,
		envelopeFrom:    "sender@example.com",
		recipients:      []string{"recipient@example.net"},
		delivery:        &submissionDeliverySpy{},
		maxMessageBytes: 1024,
	}
	require.ErrorIs(t, session.Data(submissionDataErrorReader{err: smtpserver.ErrDataTooLarge}), smtpserver.ErrDataTooLarge)
}
