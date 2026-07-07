package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateReferralInviteCodeUsesTenRandomCharactersAfterAFFPrefix(t *testing.T) {
	code, err := generateReferralInviteCode()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(code, referralInviteCodePrefix))
	require.Len(t, code, len(referralInviteCodePrefix)+referralInviteRandomCodeLength)
	require.Len(t, strings.TrimPrefix(code, referralInviteCodePrefix), referralInviteRandomCodeLength)
}
