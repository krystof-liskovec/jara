package jara

import (
	"fmt"
	"path"
	"strings"
	"sync"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/rivo/uniseg"
)

// Code in diffs and fenced blocks is highlighted line by line. Spans keep the
// token type, not a color, so cached lines repaint correctly in light and
// dark themes; colors come from Chroma's GitHub styles at paint time.

type chatSyntaxSpan struct {
	text  string
	token chroma.TokenType
}

const chatSyntaxCacheLimit = 32768 // above the changes pane maximum of 30 files × 400 lines

var chatSyntaxCache = struct {
	sync.Mutex
	lexers map[string]chroma.Lexer
	lines  map[string][]chatSyntaxSpan
}{lexers: map[string]chroma.Lexer{}, lines: map[string][]chatSyntaxSpan{}}

// chatSyntaxLexer finds a lexer for a fence language ("go") or a file name
// ("internal/app.go"), or nil for plain text.
func chatSyntaxLexer(name string) chroma.Lexer {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "text" || name == "plain" || name == "diff" {
		return nil
	}
	chatSyntaxCache.Lock()
	defer chatSyntaxCache.Unlock()
	if lexer, ok := chatSyntaxCache.lexers[name]; ok {
		return lexer
	}
	lexer := lexers.Get(name)
	if lexer == nil {
		lexer = lexers.Match(path.Base(name))
	}
	if lexer != nil {
		lexer = chroma.Coalesce(lexer)
	}
	chatSyntaxCache.lexers[name] = lexer
	return lexer
}

// chatSyntaxSpans tokenizes one line of code; nil means no highlighting.
func chatSyntaxSpans(language, code string) []chatSyntaxSpan {
	lexer := chatSyntaxLexer(language)
	if lexer == nil || strings.TrimSpace(code) == "" {
		return nil
	}
	key := language + "\x00" + code
	chatSyntaxCache.Lock()
	cached, ok := chatSyntaxCache.lines[key]
	chatSyntaxCache.Unlock()
	if ok {
		return cached
	}
	var spans []chatSyntaxSpan
	if iterator, err := lexer.Tokenise(nil, code); err == nil {
		for _, token := range iterator.Tokens() {
			text := strings.TrimRight(token.Value, "\n")
			if text != "" {
				spans = append(spans, chatSyntaxSpan{text: text, token: token.Type})
			}
		}
	}
	chatSyntaxCache.Lock()
	if len(chatSyntaxCache.lines) >= chatSyntaxCacheLimit {
		chatSyntaxCache.lines = map[string][]chatSyntaxSpan{}
	}
	chatSyntaxCache.lines[key] = spans
	chatSyntaxCache.Unlock()
	return spans
}

// chatDiffSyntax splits a unified-diff line into its kind and the highlighted
// code after the +, -, or space marker.
func chatDiffSyntax(file, raw string) (kind string, spans []chatSyntaxSpan) {
	switch {
	case strings.HasPrefix(raw, "@@"):
		return "hunk", nil
	case strings.HasPrefix(raw, "+++ "), strings.HasPrefix(raw, "--- "), strings.HasPrefix(raw, "diff --git"), strings.HasPrefix(raw, "index "):
		return "meta", nil
	case strings.HasPrefix(raw, "+"):
		kind = "add"
	case strings.HasPrefix(raw, "-"):
		kind = "delete"
	case strings.HasPrefix(raw, " "), raw == "":
		kind = "context"
	default:
		return "meta", nil
	}
	if raw == "" {
		return kind, nil
	}
	return kind, chatSyntaxSpans(file, raw[1:])
}

// wrapChatSyntaxSpans splits spans into rows of at most width cells.
func wrapChatSyntaxSpans(spans []chatSyntaxSpan, width int) [][]chatSyntaxSpan {
	width = max(1, width)
	rows := [][]chatSyntaxSpan{nil}
	used := 0
	for _, span := range spans {
		var current strings.Builder
		flush := func() {
			if current.Len() > 0 {
				rows[len(rows)-1] = append(rows[len(rows)-1], chatSyntaxSpan{text: current.String(), token: span.token})
				current.Reset()
			}
		}
		graphemes := uniseg.NewGraphemes(span.text)
		for graphemes.Next() {
			cell := max(0, graphemes.Width())
			if used+cell > width && used > 0 {
				flush()
				rows = append(rows, nil)
				used = 0
			}
			current.WriteString(graphemes.Str())
			used += cell
		}
		flush()
	}
	return rows
}

func chatSyntaxText(spans []chatSyntaxSpan) string {
	var b strings.Builder
	for _, span := range spans {
		b.WriteString(span.text)
	}
	return b.String()
}

var chatSyntaxStyles = struct {
	sync.Mutex
	colors map[string]string
}{colors: map[string]string{}}

// chatSyntaxColor returns "r;g;b" for a token, or "" to keep the line color.
func chatSyntaxColor(token chroma.TokenType, light bool) (color string, bold bool) {
	name := "github-dark"
	if light {
		name = "github"
	}
	key := fmt.Sprintf("%s/%d", name, token)
	chatSyntaxStyles.Lock()
	defer chatSyntaxStyles.Unlock()
	if cached, ok := chatSyntaxStyles.colors[key]; ok {
		color, boldText, _ := strings.Cut(cached, "|")
		return color, boldText == "b"
	}
	entry := styles.Get(name).Get(token)
	text := styles.Get(name).Get(chroma.Text)
	if entry.Colour.IsSet() && entry.Colour != text.Colour {
		color = fmt.Sprintf("%d;%d;%d", entry.Colour.Red(), entry.Colour.Green(), entry.Colour.Blue())
	}
	bold = entry.Bold == chroma.Yes
	flag := ""
	if bold {
		flag = "b"
	}
	chatSyntaxStyles.colors[key] = color + "|" + flag
	return color, bold
}

// paintChatSyntax writes prefix in prefixFG, then the spans in their syntax
// colors (defaultFG where a token has none), clipped and padded to width.
func paintChatSyntax(prefix string, spans []chatSyntaxSpan, width int, bg, prefixFG, defaultFG, style string, light bool) string {
	var b strings.Builder
	b.WriteString(menuBG(bg) + menuFG(prefixFG) + style)
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
		for _, span := range spans {
			color, bold := chatSyntaxColor(span.token, light)
			b.WriteString(menuFG(textutil.FirstNonEmpty(color, defaultFG)))
			if bold {
				b.WriteString("\x1b[1m")
			}
			full := write(span.text)
			if bold {
				b.WriteString("\x1b[22m" + style)
			}
			if !full {
				break
			}
		}
	}
	b.WriteString(strings.Repeat(" ", max(0, width-used)) + "\x1b[0m")
	return b.String()
}
