package msacl

import (
	"testing"

	maildomain "github.com/donnel666/remail/internal/mailtransport/domain"
	"github.com/stretchr/testify/assert"
)

func TestMapAuthErrorKeepsReferenceStatusGranularity(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		category    string
		safeMessage string
	}{
		{
			name:        "password",
			err:         newAuthError("密码错误", AuthStatusPasswordError),
			category:    "password",
			safeMessage: "Microsoft account password is incorrect.",
		},
		{
			name:        "unknown mailbox",
			err:         newAuthError("账号不存在", AuthStatusUnknownMailbox),
			category:    "unknown_mailbox",
			safeMessage: "Microsoft account does not exist or recovery mailbox is not supported.",
		},
		{
			name:        "mfa",
			err:         newAuthError("需要两步验证", AuthStatusMFARequired),
			category:    "mfa",
			safeMessage: "Microsoft account requires authenticator verification.",
		},
		{
			name:        "passkey",
			err:         newAuthError("需要通行密钥", AuthStatusPasskeyRequired),
			category:    "passkey",
			safeMessage: "Microsoft account requires passkey verification.",
		},
		{
			name:        "phone",
			err:         newAuthError("需要手机验证", AuthStatusPhoneVerification),
			category:    "phone",
			safeMessage: "Microsoft account requires phone verification.",
		},
		{
			name:        "locked",
			err:         newAuthError("账号已锁定", AuthStatusAccountLocked),
			category:    "locked",
			safeMessage: "Microsoft account is locked.",
		},
		{
			name:        "account abnormal",
			err:         newAuthError("账号异常", AuthStatusAccountAbnormal),
			category:    "account_abnormal",
			safeMessage: "Microsoft account is restricted or requires recovery.",
		},
		{
			name:        "code timeout",
			err:         newAuthError("验证码超时", AuthStatusCodeTimeout, "bind@example.com"),
			category:    "code_timeout",
			safeMessage: "Auxiliary mailbox verification code was not received in time.",
		},
		{
			name:        "code error",
			err:         newAuthError("验证码错误", AuthStatusVerifyCodeError, "bind@example.com"),
			category:    "code_error",
			safeMessage: "Auxiliary mailbox verification code is incorrect or expired.",
		},
		{
			name:        "auth timeout",
			err:         newAuthError("授权超时", AuthStatusAuthTimeout),
			category:    "auth_timeout",
			safeMessage: "Microsoft authorization timed out.",
		},
		{
			name:        "request",
			err:         newAuthError("请求异常", AuthStatusRequestError),
			category:    "request",
			safeMessage: "Microsoft authorization request failed temporarily.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapAuthError(tt.err, true)

			assert.False(t, result.Valid)
			assert.Equal(t, tt.category, result.Category)
			assert.Equal(t, tt.safeMessage, result.SafeMessage)
		})
	}
}

func TestMapAuthErrorAlreadyBoundPreservesBindingStateWithoutLeakingDisplay(t *testing.T) {
	result := mapAuthError(&AuthError{
		Message:      "已绑定辅助邮箱(masked@example.com)",
		Status:       AuthStatusAlreadyBound,
		BoundMailbox: "binding@example.com",
		BoundDisplay: "masked@example.com",
	}, false)

	assert.False(t, result.Valid)
	assert.Equal(t, "already_bound", result.Category)
	assert.Equal(t, "Microsoft account is already bound to another recovery mailbox.", result.SafeMessage)
	assert.Equal(t, "binding@example.com", result.BindingAddress)
	assert.Equal(t, string(maildomain.MicrosoftBindingFailed), result.BindingStatus)
	assert.NotContains(t, result.SafeMessage, "masked@example.com")
}
