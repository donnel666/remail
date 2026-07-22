package app

import (
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateRegistrationEmail(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateRegistrationEmail("User123@example.com"))
	require.NoError(t, validateRegistrationEmail("  alice@outlook.com  "))
	require.NoError(t, validateRegistrationEmail("user@vip.qq.com"))
	require.NoError(t, validateRegistrationEmail("user@sub.qq.com"))

	require.ErrorIs(t, validateRegistrationEmail("a.b@example.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("a_b@example.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("a+b@example.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("用户@example.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("not-an-email"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("@example.com"), domain.ErrRegistrationEmailLocalInvalid)

	require.ErrorIs(t, validateRegistrationEmail("user@qq.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@foxmail.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@google.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@proton.me"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@protonmail.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@pm.me"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@mail.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("User@QQ.COM"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@qq.com."), domain.ErrRegistrationEmailDomainBlocked)
}
