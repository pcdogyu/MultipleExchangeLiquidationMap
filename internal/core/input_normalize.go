package liqmap

import (
	"strconv"
	"strings"
)

func normalizeQuotedInput(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Try to undo accidental quoting/escaping like "\"value\"".
	for i := 0; i < 2; i++ {
		if unq, err := strconv.Unquote(s); err == nil {
			s = strings.TrimSpace(unq)
			continue
		}
		break
	}
	for len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			s = strings.TrimSpace(s[1 : len(s)-1])
			continue
		}
		break
	}
	// Common case: backslashes were persisted literally.
	s = strings.ReplaceAll(s, "\\\"", "\"")
	return strings.TrimSpace(s)
}

func normalizeCSVInput(raw string) string {
	return normalizeQuotedInput(raw)
}
