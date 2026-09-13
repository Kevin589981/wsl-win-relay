// Package diagnostic normalizes untrusted error text before it crosses a
// bounded protocol or persistence boundary.
package diagnostic

import (
	"strings"
	"unicode/utf8"
)

// UTF8 returns valid UTF-8 no longer than maxBytes without splitting a rune.
func UTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maxBytes {
		return value
	}
	value = value[:maxBytes]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

// SingleLine additionally removes line framing characters before bounding the
// text. Replacing instead of deleting them keeps adjacent words separated.
func SingleLine(value string, maxBytes int) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	return UTF8(value, maxBytes)
}
