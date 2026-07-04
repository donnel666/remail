package infra

import (
	"context"
	"io"
	"strings"
	"testing"

	mailapp "github.com/donnel666/remail/internal/mailtransport/app"
	"github.com/donnel666/remail/internal/mailtransport/domain"
	smtpserver "github.com/emersion/go-smtp"
	"github.com/stretchr/testify/require"
)

type inboundAccepterSpy struct {
	accepted    bool
	contentSize int64
	content     string
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
