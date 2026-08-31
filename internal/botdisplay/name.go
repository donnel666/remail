package botdisplay

import (
	"strings"
	"unicode"
)

// Name returns the user-chosen nickname or a stable anonymous label. Bot
// responses must never fall back to an email address or an internal user ID.
func Name(nickname string, _ uint) string {
	if nickname = strings.TrimSpace(nickname); safeNickname(nickname) {
		return nickname
	}
	return "匿名用户"
}

func safeNickname(value string) bool {
	if value == "" || strings.Contains(value, "@") {
		return false
	}
	onlyDigits := true
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
		onlyDigits = onlyDigits && unicode.IsDigit(char)
	}
	return !onlyDigits
}
