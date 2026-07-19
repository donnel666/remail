package infra

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	smtpserver "github.com/emersion/go-smtp"
	"github.com/stretchr/testify/require"
)

type inboundAccepterSpy struct {
	accepted     bool
	contentSize  int64
	content      string
	closeContent bool
}

func (s *inboundAccepterSpy) ResolveRecipient(context.Context, string) (*domain.InboundRecipient, error) {
	return &domain.InboundRecipient{
		Email:        "code@example.com",
		ResourceID:   1,
		ResourceType: domain.InboundResourceDomain,
		OwnerUserID:  1,
	}, nil
}

func (s *inboundAccepterSpy) Accept(_ context.Context, message mailapp.InboundRawMessage) ([]domain.InboundMail, error) {
	s.accepted = true
	s.contentSize = message.ContentSize
	if message.Content != nil {
		content, _ := io.ReadAll(message.Content)
		s.content = string(content)
		if s.closeContent {
			if closer, ok := message.Content.(io.Closer); ok {
				if err := closer.Close(); err != nil {
					return nil, err
				}
			}
		}
	}
	return nil, nil
}

func TestInboundSMTPSessionRejectsOversizedMessageBeforeAccept(t *testing.T) {
	accepter := &inboundAccepterSpy{}
	session := &inboundSMTPSession{
		accepter:        accepter,
		maxMessageBytes: 5,
		envelopeFrom:    "sender@example.com",
		recipients: []domain.InboundRecipient{{
			Email:        "code@example.com",
			ResourceID:   1,
			ResourceType: domain.InboundResourceDomain,
			OwnerUserID:  1,
		}},
	}

	err := session.Data(strings.NewReader("123456"))
	require.Error(t, err)
	var smtpErr *smtpserver.SMTPError
	require.ErrorAs(t, err, &smtpErr)
	require.Equal(t, 552, smtpErr.Code)
	require.False(t, accepter.accepted)
}

func TestInboundSMTPSessionStreamsAcceptedMessage(t *testing.T) {
	accepter := &inboundAccepterSpy{}
	session := &inboundSMTPSession{
		accepter:        accepter,
		maxMessageBytes: 64,
		envelopeFrom:    "sender@example.com",
		recipients: []domain.InboundRecipient{{
			Email:        "code@example.com",
			ResourceID:   1,
			ResourceType: domain.InboundResourceDomain,
			OwnerUserID:  1,
		}},
	}

	payload := "Subject: test\r\n\r\nhello"
	err := session.Data(strings.NewReader(payload))
	require.NoError(t, err)
	require.True(t, accepter.accepted)
	require.Equal(t, int64(len(payload)), accepter.contentSize)
	require.Equal(t, payload, accepter.content)
}

func TestInboundSMTPSessionCleanupFailureDoesNotRejectAcceptedMessage(t *testing.T) {
	var logs bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previousLogger) })

	accepter := &inboundAccepterSpy{closeContent: true}
	session := &inboundSMTPSession{
		accepter:        accepter,
		maxMessageBytes: 64,
		envelopeFrom:    "sender@example.com",
		recipients: []domain.InboundRecipient{{
			Email:        "code@example.com",
			ResourceID:   1,
			ResourceType: domain.InboundResourceDomain,
			OwnerUserID:  1,
		}},
	}

	err := session.Data(strings.NewReader("Subject: test\r\n\r\nhello"))
	require.NoError(t, err)
	require.True(t, accepter.accepted)
	require.Contains(t, logs.String(), "inbound smtp temporary message cleanup failed")
	require.Contains(t, logs.String(), "close:")
}

func TestCleanupInboundSMTPTempFileReportsRemoveFailure(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "non-empty")
	require.NoError(t, os.Mkdir(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "message.eml"), []byte("mail"), 0o600))
	file, err := os.Open(directory)
	require.NoError(t, err)

	err = cleanupInboundSMTPTempFile(file)
	require.ErrorContains(t, err, "remove:")
}
