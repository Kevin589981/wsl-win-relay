package diagnostic

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestUTF8BoundsWithoutSplittingRune(t *testing.T) {
	got := UTF8(strings.Repeat("\u754c", 4), 8)
	if got != "\u754c\u754c" || len(got) != 6 || !utf8.ValidString(got) {
		t.Fatalf("bounded value=%q length=%d", got, len(got))
	}
}

func TestUTF8ReplacesInvalidInputBeforeBounding(t *testing.T) {
	if got := UTF8(string([]byte{'x', 0xff, 'y'}), 5); got != "x\uFFFDy" {
		t.Fatalf("normalized value=%q", got)
	}
	if got := UTF8("value", 0); got != "" {
		t.Fatalf("zero-budget value=%q", got)
	}
}

func TestSingleLineNormalizesLineEndings(t *testing.T) {
	if got := SingleLine("first\r\nsecond", 100); got != "first  second" {
		t.Fatalf("single-line value=%q", got)
	}
}
