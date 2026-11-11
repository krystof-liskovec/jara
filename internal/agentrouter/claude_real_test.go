package agentrouter

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentfake"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// TestInstalledClaudeCodeThroughTheRouter drives the real Claude Code CLI
// through the agent router, checking the stream-json shapes Jara relies on.
// It sends a few short prompts to Claude Haiku on the logged-in account.
func TestInstalledClaudeCodeThroughTheRouter(t *testing.T) {
	if os.Getenv("JARA_REAL_CLAUDE_TEST") != "1" {
		t.Skip("set JARA_REAL_CLAUDE_TEST=1 to check the installed, logged-in Claude Code")
	}
	binary, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	agentfake.Setup(t) // Codex stays fake; only Claude Code is real
	t.Setenv("JARA_CLAUDE_BIN", binary)
	h := startRouterHarness(t, protocol.AgentClaude)
	h.Wait = 3 * time.Minute
	h.Response(h.Request("initialize", map[string]any{}))
	cwd := t.TempDir()
	started := h.Response(h.Request("thread/start", map[string]any{"cwd": cwd}))
	thread := started["result"].(map[string]any)["thread"].(map[string]any)["id"].(string)
	turn := func(input ...any) []map[string]any {
		h.Request("turn/start", map[string]any{"threadId": thread, "model": "claude/haiku", "input": input})
		done, seen := h.WaitFor(agentfake.IsMethod("turn/completed"))
		if status := done["params"].(map[string]any)["turn"].(map[string]any)["status"]; status != "completed" {
			t.Fatalf("turn %v: %v", status, done)
		}
		return seen
	}

	seen := turn(textInput("Reply with exactly the word PONG and nothing else."))
	if reply := agentfake.AgentMessages(seen); !strings.Contains(reply, "PONG") {
		t.Fatalf("reply: %q", reply)
	}
	var resolved, usage bool
	for _, m := range seen {
		resolved = resolved || m["method"] == "jara/model/resolved"
		if m["method"] == "thread/tokenUsage/updated" {
			tokens := m["params"].(map[string]any)["tokenUsage"].(map[string]any)
			usage = tokens["last"].(map[string]any)["totalTokens"].(float64) > 0 && tokens["modelContextWindow"].(float64) > 0
		}
	}
	if !resolved || !usage {
		t.Fatalf("Claude Code must report its model (%v) and context usage (%v)", resolved, usage)
	}

	seen = turn(textInput("What single color fills [Image #1]? Answer with one lowercase word."), redImageInput(t))
	if reply := strings.ToLower(agentfake.AgentMessages(seen)); !strings.Contains(reply, "red") {
		t.Fatalf("image reply: %q", reply)
	}

	btw := h.Response(h.Request("jara/btw", map[string]any{"threadId": thread, "question": "Which word did you reply with first? One word."}))
	if answer, _ := btw["result"].(map[string]any)["answer"].(string); !strings.Contains(strings.ToUpper(answer), "PONG") {
		t.Fatalf("side question: %v", btw)
	}

	if compact := h.Response(h.Request("thread/compact/start", map[string]any{"threadId": thread})); compact["error"] != nil {
		t.Fatalf("compact: %v", compact)
	}
	h.WaitFor(func(m map[string]any) bool {
		item, _ := m["params"].(map[string]any)["item"].(map[string]any)
		return m["method"] == "item/completed" && item["type"] == "contextCompaction"
	})
	h.WaitFor(agentfake.IsMethod("turn/completed"))

	if rewind := h.Response(h.Request("jara/rewind", map[string]any{"threadId": thread, "drop": 1})); rewind["error"] != nil {
		t.Fatalf("rewind: %v", rewind)
	}
	seen = turn(textInput("Did I show you an image in this conversation? Answer YES or NO only."))
	if reply := strings.ToUpper(agentfake.AgentMessages(seen)); !strings.Contains(reply, "NO") {
		t.Fatalf("after rewinding past the image the agent must not know it: %q", reply)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("JARA_DATA"), "agent-threads", thread+".json")); err != nil {
		t.Fatalf("the conversation record is saved: %v", err)
	}
}

func redImageInput(t *testing.T) map[string]any {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for x := range 32 {
		for y := range 32 {
			img.Set(x, y, color.RGBA{R: 220, G: 20, B: 20, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"type": "image", "url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes())}
}
