package jara

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Replies show their inline markdown as styling instead of markers:
// **bold**, *italic*, `code` and [links](url). What the user typed and tool
// output stay exactly as written.

type chatInlineStyle uint8

const (
	chatInlineBold chatInlineStyle = 1 << iota
	chatInlineItalic
	chatInlineCode
	chatInlineLink
	chatInlineURL
)

// chatInlineRole reports whether a role's text is markdown to style.
func chatInlineRole(role string) bool {
	return role != "user" && role != "tool"
}

// wrapChatProse wraps a line of prose, styling its inline markdown.
func wrapChatProse(text string, width int, prefix, continuation, kind, role string) []chatViewLine {
	if !chatInlineRole(role) {
		return wrapChatLine(text, width, prefix, continuation, kind, role, false)
	}
	spans, styled := chatInlineSpans(text)
	if !styled {
		return wrapChatLine(text, width, prefix, continuation, kind, role, false)
	}
	rows := wrapChatInline(spans, max(1, width-uniseg.StringWidth(prefix)), max(1, width-uniseg.StringWidth(continuation)))
	lines := make([]chatViewLine, 0, len(rows))
	for i, row := range rows {
		linePrefix := prefix
		if i > 0 {
			linePrefix = continuation
		}
		lines = append(lines, chatViewLine{text: linePrefix + chatSyntaxText(row), kind: kind, role: role, spans: row, codeStart: len(linePrefix)})
	}
	return lines
}

// chatInlineSpans splits a line into styled runs without their markers and
// reports whether it had any markdown.
func chatInlineSpans(text string) ([]chatSyntaxSpan, bool) {
	var p chatInlineParser
	p.parse(text, 0)
	return p.spans, p.styled
}

type chatInlineParser struct {
	spans  []chatSyntaxSpan
	styled bool
}

func (p *chatInlineParser) add(text string, style chatInlineStyle) {
	if text == "" {
		return
	}
	if n := len(p.spans); n > 0 && p.spans[n-1].style == style {
		p.spans[n-1].text += text
		return
	}
	p.spans = append(p.spans, chatSyntaxSpan{text: text, style: style})
}

func (p *chatInlineParser) parse(text string, style chatInlineStyle) {
	plain := 0 // where the text not yet added starts
	emit := func(from, to int, inner string, innerStyle chatInlineStyle, nested bool) {
		p.add(text[plain:from], style)
		if nested {
			p.parse(inner, innerStyle)
		} else {
			p.add(inner, innerStyle)
		}
		p.styled, plain = true, to
	}
	for i := 0; i < len(text); {
		switch c := text[i]; c {
		case '\\':
			if i+1 < len(text) && strings.IndexByte("\\`*_[]()", text[i+1]) >= 0 {
				emit(i, i+2, text[i+1:i+2], style, false)
				i += 2
				continue
			}
		case '`':
			run := chatInlineRun(text, i, '`')
			if end := chatInlineCodeEnd(text, i+run, run); end >= 0 {
				code := text[i+run : end]
				if len(code) > 2 && code[0] == ' ' && code[len(code)-1] == ' ' && strings.TrimSpace(code) != "" {
					code = code[1 : len(code)-1]
				}
				emit(i, end+run, code, style|chatInlineCode, false)
				i = end + run
				continue
			}
			i += run
			continue
		case '*', '_':
			run := chatInlineRun(text, i, c)
			if end, size := chatInlineEmphasisAt(text, i, run); end >= 0 {
				add := chatInlineItalic
				if size == 2 {
					add = chatInlineBold
				}
				emit(i, end+size, text[i+size:end], style|add, true)
				i = end + size
				continue
			}
			i += run
			continue
		case '[':
			if label, url, end, ok := chatInlineLinkAt(text, i); ok {
				emit(i, end, label, style|chatInlineLink, true)
				if url != label {
					p.add(" ("+url+")", style|chatInlineURL)
				}
				i = end
				continue
			}
		}
		i++
	}
	p.add(text[plain:], style)
}

func chatInlineRun(text string, at int, c byte) int {
	run := 0
	for at+run < len(text) && text[at+run] == c {
		run++
	}
	return run
}

// chatInlineCodeEnd finds the backticks closing a code span: a run of the
// same length.
func chatInlineCodeEnd(text string, from, run int) int {
	for i := from; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		if length := chatInlineRun(text, i, '`'); length == run {
			return i
		} else {
			i += length
		}
	}
	return -1
}

// chatInlineEmphasisAt finds emphasis opening with the run of markers at i,
// trying bold before italic: where the closing markers start and how many
// there are, or -1.
func chatInlineEmphasisAt(text string, i, run int) (end, size int) {
	for _, size := range []int{2, 1} {
		if run >= size && chatInlineOpens(text, i, size) {
			if end := chatInlineEmphasisEnd(text, i+size, text[i:i+size]); end >= 0 {
				return end, size
			}
		}
	}
	return -1, 0
}

// chatInlineOpens reports whether size markers at i open emphasis: they
// start a word and are followed by text, so snake_case and 2 * 3 stay as
// written.
func chatInlineOpens(text string, i, size int) bool {
	if i > 0 && chatInlineWordByte(text[:i], true) {
		return false
	}
	next, _ := utf8.DecodeRuneInString(text[i+size:])
	return i+size < len(text) && !unicode.IsSpace(next) && (size == 2 || unicode.IsLetter(next) || unicode.IsDigit(next) || next == '`')
}

// chatInlineEmphasisEnd finds the marker that closes emphasis: it follows
// text and ends a word.
func chatInlineEmphasisEnd(text string, from int, marker string) int {
	for i := from + 1; i+len(marker) <= len(text); i++ {
		if text[i:i+len(marker)] != marker || text[i-1] == ' ' || text[i-1] == marker[0] {
			continue
		}
		after := i + len(marker)
		if after < len(text) && (text[after] == marker[0] || chatInlineWordByte(text[after:], false)) {
			continue
		}
		return i
	}
	return -1
}

// chatInlineWordByte reports whether the rune at the end (or the start) of
// text is part of a word.
func chatInlineWordByte(text string, last bool) bool {
	var r rune
	if last {
		r, _ = utf8.DecodeLastRuneInString(text)
	} else {
		r, _ = utf8.DecodeRuneInString(text)
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// chatInlineLinkAt reads [label](url) at i.
func chatInlineLinkAt(text string, i int) (label, url string, end int, ok bool) {
	close := strings.IndexByte(text[i+1:], ']')
	if close <= 0 {
		return "", "", 0, false
	}
	close += i + 1
	if close+1 >= len(text) || text[close+1] != '(' {
		return "", "", 0, false
	}
	paren := strings.IndexByte(text[close+2:], ')')
	if paren <= 0 {
		return "", "", 0, false
	}
	url = text[close+2 : close+2+paren]
	if strings.ContainsAny(url, " \t") {
		return "", "", 0, false
	}
	return text[i+1 : close], url, close + 3 + paren, true
}

// wrapChatInline word-wraps styled runs as wrapChatWords wraps text: spaces
// between words become one, and a word longer than a row is cut.
func wrapChatInline(spans []chatSyntaxSpan, firstWidth, nextWidth int) [][]chatSyntaxSpan {
	type word struct {
		spans []chatSyntaxSpan
		width int
		gap   chatInlineStyle // style of the space before the word
	}
	var words []word
	var current word
	inWord, gap := false, chatInlineStyle(0)
	for _, span := range spans {
		for text := span.text; text != ""; {
			if text[0] == ' ' {
				text = strings.TrimLeft(text, " ")
				if inWord {
					words, current, inWord = append(words, current), word{}, false
				}
				gap = span.style
				continue
			}
			end := strings.IndexByte(text, ' ')
			if end < 0 {
				end = len(text)
			}
			if !inWord {
				current.gap, inWord = gap, true
			}
			current.spans = appendChatInline(current.spans, text[:end], span.style)
			current.width += uniseg.StringWidth(text[:end])
			text = text[end:]
		}
	}
	if inWord {
		words = append(words, current)
	}
	rows := [][]chatSyntaxSpan{nil}
	used, width := 0, firstWidth
	for _, w := range words {
		last := len(rows) - 1
		if used > 0 && used+1+w.width <= width {
			rows[last] = appendChatInline(rows[last], " ", w.gap)
			for _, span := range w.spans {
				rows[last] = appendChatInline(rows[last], span.text, span.style)
			}
			used += 1 + w.width
			continue
		}
		if used > 0 {
			rows, used, width = append(rows, nil), 0, nextWidth
			last++
		}
		if w.width <= width {
			rows[last], used = w.spans, w.width
			continue
		}
		parts := wrapChatSyntaxSpans(w.spans, width)
		rows = append(rows[:last], parts...)
		used = uniseg.StringWidth(chatSyntaxText(parts[len(parts)-1]))
	}
	return rows
}

func appendChatInline(spans []chatSyntaxSpan, text string, style chatInlineStyle) []chatSyntaxSpan {
	if n := len(spans); n > 0 && spans[n-1].style == style {
		spans[n-1].text += text
		return spans
	}
	return append(spans, chatSyntaxSpan{text: text, style: style})
}

// paintChatInline paints a prose row whose spans carry inline styles, in the
// row's own colors.
func paintChatInline(prefix string, line chatViewLine, width int, palette menuPalette) string {
	bg, fg, base := chatViewLineStyle(line.kind, line.role, false, palette)
	var b strings.Builder
	b.WriteString(menuBG(bg) + menuFG(fg) + base)
	used := 0
	write := func(text string) bool {
		graphemes := uniseg.NewGraphemes(text)
		for graphemes.Next() {
			if used+graphemes.Width() > width {
				return false
			}
			b.WriteString(graphemes.Str())
			used += graphemes.Width()
		}
		return true
	}
	if write(prefix) {
		for _, span := range line.spans {
			b.WriteString(chatInlineSGR(span.style, palette))
			full := write(span.text)
			b.WriteString("\x1b[22;23;24m" + menuFG(fg) + base)
			if !full {
				break
			}
		}
	}
	b.WriteString(strings.Repeat(" ", max(0, width-used)) + "\x1b[0m")
	return b.String()
}

func chatInlineSGR(style chatInlineStyle, palette menuPalette) string {
	var b strings.Builder
	switch {
	case style&chatInlineCode != 0:
		b.WriteString(menuFG(palette.green))
	case style&chatInlineURL != 0:
		b.WriteString(menuFG(palette.muted))
	}
	if style&chatInlineBold != 0 {
		b.WriteString("\x1b[1m")
	}
	if style&chatInlineItalic != 0 {
		b.WriteString("\x1b[3m")
	}
	if style&chatInlineLink != 0 {
		b.WriteString("\x1b[4m")
	}
	return b.String()
}
