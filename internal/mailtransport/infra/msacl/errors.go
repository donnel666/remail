package msacl

const (
	AuthStatusSuccess           = "授权成功"
	AuthStatusPasswordError     = "密码错误"
	AuthStatusAccountAbnormal   = "账号异常"
	AuthStatusUnknownMailbox    = "未知邮箱"
	AuthStatusCodeTimeout       = "验证码超时"
	AuthStatusVerifyCodeError   = "验证码错误"
	AuthStatusAuthTimeout       = "授权超时"
	AuthStatusAlreadyBound      = "已绑定辅助邮箱"
	AuthStatusRequestError      = "请求异常"
	AuthStatusUnknownError      = "未知错误"
	AuthStatusMFARequired       = "需要两步验证"
	AuthStatusPhoneVerification = "需要手机验证"
	AuthStatusPasskeyRequired   = "需要通行密钥"
	AuthStatusAccountLocked     = "账号已锁定"
	AuthStatusRateLimited       = "频率受限"
)

type AuthError struct {
	Message      string
	Status       string
	BoundMailbox string
	BoundDisplay string
	Cause        error
}

func (e *AuthError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *AuthError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newAuthError(message string, status ...string) *AuthError {
	err := &AuthError{Message: message}
	if len(status) > 0 {
		err.Status = status[0]
	}
	if len(status) > 1 {
		err.BoundMailbox = status[1]
	}
	if len(status) > 2 {
		err.BoundDisplay = status[2]
	}
	return err
}

func wrapAuthError(message string, status string, cause error, boundMailbox ...string) *AuthError {
	err := &AuthError{Message: message, Status: status, Cause: cause}
	if len(boundMailbox) > 0 {
		err.BoundMailbox = boundMailbox[0]
	}
	return err
}
