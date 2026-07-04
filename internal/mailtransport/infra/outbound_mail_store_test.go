package infra

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
)

func TestOutboundMailModelRoundTripKeepsMessageBody(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	sentAt := now.Add(time.Minute)
	mail := domain.NewOutboundMail(domain.OutboundMessage{
		IdempotencyKey: "mail-1",
		Purpose:        domain.PurposeVerificationCode,
		From:           "no-reply@example.com",
		To:             "user@example.com",
		Subject:        "ReMail 邮箱验证码",
		TextBody:       "text body",
		HTMLBody:       "<p>html body</p>",
	}, now)
	mail.ID = 10
	mail.MarkSent(sentAt)

	model := outboundMailModel(mail)
	roundTrip := outboundMailFromModel(*model)

	assert.Equal(t, mail.ID, roundTrip.ID)
	assert.Equal(t, mail.IdempotencyKey, roundTrip.IdempotencyKey)
	assert.Equal(t, mail.RequestHash, roundTrip.RequestHash)
	assert.Equal(t, mail.Purpose, roundTrip.Purpose)
	assert.Equal(t, mail.Sender, roundTrip.Sender)
	assert.Equal(t, mail.Recipient, roundTrip.Recipient)
	assert.Equal(t, mail.Subject, roundTrip.Subject)
	assert.Equal(t, mail.TextBody, roundTrip.TextBody)
	assert.Equal(t, mail.HTMLBody, roundTrip.HTMLBody)
	assert.Equal(t, mail.Status, roundTrip.Status)
	assert.Equal(t, mail.Retries, roundTrip.Retries)
	assert.NotNil(t, roundTrip.SentAt)
	assert.True(t, sentAt.Equal(*roundTrip.SentAt))
}
