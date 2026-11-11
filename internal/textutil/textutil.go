// Package textutil holds small helpers for text shown in the terminal.
package textutil

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/rivo/uniseg"
)

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func TailText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit > 0 && len(text) > limit {
		return "… (earlier output omitted)\n" + text[len(text)-limit:]
	}
	return text
}

var TerminalEscapes = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\))`)

// Names originate in repositories and must not control the terminal.
func SanitizeText(s string) string {
	s = TerminalEscapes.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		if r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 {
			return -1
		}
		return r
	}, s)
}

func FitText(s string, cells int) string {
	if cells <= 0 {
		return ""
	}
	s = SanitizeText(s)
	if uniseg.StringWidth(s) <= cells {
		return s
	}
	var b strings.Builder
	used := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		if used+g.Width() > cells-1 {
			break
		}
		b.WriteString(g.Str())
		used += g.Width()
	}
	return b.String() + "…"
}
