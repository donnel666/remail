package infra

import (
	"testing"

	"github.com/donnel666/remail/internal/platform"
	"github.com/stretchr/testify/require"
)

func TestConfiguredSenderSharesDirectRelayAndDKIMValidation(t *testing.T) {
	direct, err := NewConfiguredSMTPDelivery(platform.SMTPConfig{Mode: "direct", From: "sender@example.com"})
	require.NoError(t, err)
	require.IsType(t, &DirectSMTPDelivery{}, direct)
	relay, err := NewConfiguredSMTPDelivery(platform.SMTPConfig{Mode: "relay", Addr: "localhost:2525"})
	require.NoError(t, err)
	require.IsType(t, &SMTPDelivery{}, relay)
	_, err = NewConfiguredSMTPDelivery(platform.SMTPConfig{DKIMEnabled: true})
	require.Error(t, err)
}
