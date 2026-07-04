package msacl

import "strings"

func stripProgressPrefix(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] != '[' {
		return line
	}
	end := strings.Index(line, "]")
	if end < 0 || end+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[end+1:])
}
