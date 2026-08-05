package mailbox

import "strings"

// Address returns a lower-cased lookup address and its local-mailbox bucket key.
// Callers must persist the original envelope recipient separately.
func Address(value string) (lookup, key string, ok bool) {
	lookup = strings.ToLower(strings.TrimSpace(value))
	local, host, found := strings.Cut(lookup, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") {
		return "", "", false
	}
	if plus := strings.IndexByte(local, '+'); plus >= 0 {
		local = local[:plus]
	}
	local = strings.ReplaceAll(local, ".", "")
	if local == "" {
		return "", "", false
	}
	return lookup, local + "@" + host, true
}

func Normalize(value string) string {
	_, key, _ := Address(value)
	return key
}

// AliasForms returns the exact address, the address without a plus tag, and
// the dot-insensitive address. It is shared by mailbox matching and SMTP
// mailbox routing so both sides use the same address rules.
func AliasForms(value string) (exact, plusBase, dotBase string, ok bool) {
	exact = strings.ToLower(strings.TrimSpace(value))
	local, host, found := strings.Cut(exact, "@")
	if !found || local == "" || host == "" || strings.Contains(host, "@") {
		return "", "", "", false
	}
	plusLocal := local
	if plus := strings.IndexByte(plusLocal, '+'); plus >= 0 {
		plusLocal = plusLocal[:plus]
	}
	if plusLocal == "" {
		return "", "", "", false
	}
	plusBase = plusLocal + "@" + host
	dotBase = strings.ReplaceAll(plusLocal, ".", "") + "@" + host
	return exact, plusBase, dotBase, true
}
