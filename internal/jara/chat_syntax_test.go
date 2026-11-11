package jara

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

func TestSyntaxSpansFollowTheLanguage(t *testing.T) {
	spans := chatSyntaxSpans("main.go", `func main() { return "hi" }`)
	if chatSyntaxText(spans) != `func main() { return "hi" }` {
		t.Fatalf("spans must keep the text: %q", chatSyntaxText(spans))
	}
	kinds := map[string]chroma.TokenType{}
	for _, span := range spans {
		kinds[strings.TrimSpace(span.text)] = span.token
	}
	if !kinds["func"].InCategory(chroma.Keyword) || !kinds[`"hi"`].InCategory(chroma.LiteralString) {
		t.Fatalf("Go tokens: %v", kinds)
	}
	if chatSyntaxSpans("python", "def f(): pass") == nil || chatSyntaxSpans("unknown.zzz", "x") != nil || chatSyntaxSpans("text", "x") != nil {
		t.Fatal("lexer selection")
	}
	if rows := wrapChatSyntaxSpans(spans, 10); len(rows) != 3 || chatSyntaxText(rows[0]) != "func main(" {
		t.Fatalf("wrapping: %v", rows)
	}
	for _, light := range []bool{true, false} {
		if color, _ := chatSyntaxColor(chroma.Keyword, light); color == "" {
			t.Fatal("keywords need a color")
		}
	}
}

func TestDiffPaneHighlightsCode(t *testing.T) {
	palette := menuColorsForBackground(terminalRGB{r: 30, g: 30, b: 30})
	state := &chatDiffState{}
	state.record([]chatFileChange{{Path: "app.go", Diff: "@@ -1 +1 @@\n-func old() {}\n+func new() { return }\n"}}, false)
	lines := chatDiffLines(state, 60)
	var added chatDiffLine
	for _, line := range lines {
		if line.kind == "pane-add" {
			added = line
		}
	}
	if len(added.spans) == 0 {
		t.Fatalf("added line must be highlighted: %+v", added)
	}
	painted := paintChatDiffLine(added, 60, 0, palette, true)
	keyword, _ := chatSyntaxColor(chroma.Keyword, false)
	addBG, _, _ := chatDiffColors(palette)
	if !strings.Contains(painted, menuFG(keyword)+"func") || !strings.Contains(painted, menuBG(addBG)) || !strings.Contains(painted, menuFG(palette.green)+" +") {
		t.Fatalf("painted diff line %q", painted)
	}
	if plain := terminalEscapes.ReplaceAllString(painted, ""); !strings.HasPrefix(plain, " +func new() { return }") || len([]rune(plain)) != 60 {
		t.Fatalf("plain diff line %q", plain)
	}
}

func TestTranscriptHighlightsFencedCodeAndDiffs(t *testing.T) {
	lines := expandChatMessage("Here:\n```go\nfunc main() {}\n```\n```diff app.py\n-x = 1\n+def f(): pass\n```", 60, "assistant")
	var code, removed, added chatViewLine
	for _, line := range lines {
		switch {
		case line.kind == "code" && strings.Contains(line.text, "func main"):
			code = line
		case line.kind == "code-delete":
			removed = line
		case line.kind == "code-add":
			added = line
		}
	}
	if len(code.spans) == 0 || code.text != chatCodeGutter+"func main() {}" || len(added.spans) == 0 || len(removed.spans) == 0 || added.text != chatCodeGutter+"+def f(): pass" {
		t.Fatalf("code %+v\nremoved %+v\nadded %+v", code, removed, added)
	}
	palette := menuColorsForBackground(terminalRGB{r: 250, g: 250, b: 250})
	painted := paintChatTranscriptLine(" ", added, 60, palette, true)
	keyword, _ := chatSyntaxColor(chroma.Keyword, true)
	if !strings.Contains(painted, menuFG(keyword)+"def") || !strings.HasPrefix(terminalEscapes.ReplaceAllString(painted, ""), "   │ +def f(): pass") {
		t.Fatalf("painted transcript diff %q", painted)
	}
	if text := formatChatFileChangeList("completed", []chatFileChange{{Path: "src/my app.rs", Diff: "+fn x() {}"}}); !strings.Contains(text, "```diff src/my_app.rs") {
		t.Fatalf("file changes must name the file in the fence: %q", text)
	}
}
