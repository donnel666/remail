package botdisplay

import (
	"fmt"
	"strings"
	"unicode"
)

// Name returns the same leaderboard label used by the web console: nickname,
// then the email local-part, then a stable user tag for malformed legacy rows.
func Name(nickname, email string, userID uint) string {
	if nickname = strings.TrimSpace(nickname); safeNickname(nickname) {
		return nickname
	}
	email = strings.TrimSpace(email)
	if local, _, ok := strings.Cut(email, "@"); ok && safeNickname(local) {
		return local
	}
	if safeNickname(email) {
		return email
	}
	return fmt.Sprintf("#%d", userID)
}

func safeNickname(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}
