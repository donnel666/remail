package infra

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEmailCodeMessageUsesMultipartAlternative(t *testing.T) {
	msg := emailCodeMessage("no-reply@example.com", "user@example.com", "123456")

	assert.Contains(t, msg, "Subject: ReMail verification code")
	assert.Contains(t, msg, "Content-Type: multipart/alternative")
	assert.Contains(t, msg, "Content-Type: text/plain; charset=UTF-8")
	assert.Contains(t, msg, "Content-Type: text/html; charset=UTF-8")
	assert.Contains(t, msg, "Your ReMail verification code is: 123456")
	assert.Contains(t, msg, "<h1")
	assert.Contains(t, msg, "Verification code")
	assert.True(t, strings.HasSuffix(msg, "--"+emailCodeBoundary+"--\r\n"))
}

func TestEmailCodeMessageSanitizesHeadersAndHTML(t *testing.T) {
	msg := emailCodeMessage("sender@example.com\r\nBcc: audit@example.com", "user@example.com\r\nCc: audit@example.com", "<123456>")

	assert.NotContains(t, msg, "\r\nBcc:")
	assert.NotContains(t, msg, "\r\nCc:")
	assert.Contains(t, msg, "&lt;123456&gt;")
}
