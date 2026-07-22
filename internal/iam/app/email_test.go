package app

import (
	"testing"

	"github.com/donnel666/remail/internal/iam/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateRegistrationEmail(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateRegistrationEmail("  1515445804@qq.com  "))
	require.NoError(t, validateRegistrationEmail("user@foxmail.com"))
	require.NoError(t, validateRegistrationEmail("user@gmail.com"))
	require.NoError(t, validateRegistrationEmail("user@proton.me"))
	require.NoError(t, validateRegistrationEmail("user@protonmail.com"))
	require.NoError(t, validateRegistrationEmail("user@pm.me"))
	require.NoError(t, validateRegistrationEmail("user@mail.com"))
	require.NoError(t, validateRegistrationEmail("User@QQ.COM"))

	require.ErrorIs(t, validateRegistrationEmail("first.last@gmail.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("user_name@gmail.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("user+tag@gmail.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("用户@example.com"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("not-an-email"), domain.ErrRegistrationEmailLocalInvalid)
	require.ErrorIs(t, validateRegistrationEmail("@example.com"), domain.ErrRegistrationEmailLocalInvalid)

	require.ErrorIs(t, validateRegistrationEmail("user@example.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@google.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@outlook.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@sub.qq.com"), domain.ErrRegistrationEmailDomainBlocked)
	require.ErrorIs(t, validateRegistrationEmail("user@qq.com."), domain.ErrRegistrationEmailDomainBlocked)
}
