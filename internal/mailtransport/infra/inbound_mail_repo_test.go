package infra

import (
	"testing"
	"time"

	"github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
)

func TestInboundMailModelRoundTripKeepsDispatchState(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	mail := domain.NewInboundMail("sender@test.local", domain.InboundRecipient{
		Email:        "recipient@test.local",
		ResourceID:   42,
		ResourceType: domain.InboundResourceDomain,
		OwnerUserID:  7,
	}, "mail.eml", now)
	mail.ID = 10
	mail.ProcessGeneration = 4
	mail.ProcessAttempts = 2

	roundTrip := inboundMailFromDomain(*mail).toDomain()

	assert.Equal(t, mail.ID, roundTrip.ID)
	assert.Equal(t, mail.Status, roundTrip.Status)
	assert.Equal(t, mail.ProcessGeneration, roundTrip.ProcessGeneration)
	assert.Equal(t, mail.ProcessAttempts, roundTrip.ProcessAttempts)
}
