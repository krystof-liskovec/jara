//go:build linux || darwin

package jara

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestChatMCPListsAndChangesSharedServers(t *testing.T) {
	isolateMCP(t)
	t.Setenv("JARA_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	if err := saveConfig(Config{MCPServers: map[string]sharedMCPServer{
		"docs":  {mcpServer: mcpServer{URL: "https://docs.example/mcp", Headers: map[string]string{"x-token": "secret"}}},
		"local": {mcpServer: mcpServer{Command: "notes-mcp", Args: []string{"--dir", "/n"}}, On: []string{"work"}},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chatPlace = chatPlacement{} })
	chatPlace = chatPlacement{root: "/folder"}
	term, _ := newTestChatTerminal(t)
	sent := &bytes.Buffer{}
	term.u.c = offlineChatConnection()
	term.u.c.encoder = json.NewEncoder(sent)
	term.u.thread, term.u.threadReady = "th", true

	if _, err := term.u.line("/mcp"); err != nil {
		t.Fatal(err)
	}
	screen := plainChatScreen(term.frame(140, 60, time.Now()))
	for _, want := range []string{"Shared MCP servers", "docs https://docs.example/mcp · everywhere · headers: x-token",
		"local notes-mcp --dir /n · on work · not on this device"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("missing %q:\n%s", want, screen)
		}
	}
	if strings.Contains(screen, "secret") || !strings.Contains(sent.String(), `"mcpServerStatus/list"`) {
		t.Fatalf("secret shown or status not asked: %s\n%s", sent, screen)
	}
	var request struct {
		ID json.RawMessage `json:"id"`
	}
	_ = json.Unmarshal(sent.Bytes(), &request)
	result := json.RawMessage(`{"data":[{"name":"docs","runtimeStatus":"connected","tools":{"a":{},"b":{}}},{"name":"own","runtimeStatus":"authenticationRequired"}]}`)
	if err := term.u.response(appMessage{ID: request.ID, Result: result}); err != nil {
		t.Fatal(err)
	}
	screen = plainChatScreen(term.frame(140, 60, time.Now()))
	if !strings.Contains(screen, "docs connected · 2 tools") || !strings.Contains(screen, "own needs sign-in") {
		t.Fatalf("live status:\n%s", screen)
	}

	// A change runs in the background and reports when done.
	if _, err := term.u.line(`/mcp add notes -- "notes mcp" --flag`); err != nil {
		t.Fatal(err)
	}
	select {
	case text := <-term.u.backgroundRuns:
		term.u.acceptChatBackground(text)
	case <-time.After(5 * time.Second):
		t.Fatal("the change never finished")
	}
	c, _ := readConfig()
	if got := c.MCPServers["notes"]; got.Command != "notes mcp" || strings.Join(got.Args, " ") != "--flag" || term.u.backgroundRuns != nil {
		t.Fatalf("added %+v", got)
	}
	if screen = plainChatScreen(term.frame(140, 60, time.Now())); !strings.Contains(screen, "This device: 2 MCP servers") || !strings.Contains(screen, "starts again") {
		t.Fatalf("change output:\n%s", screen)
	}
}

func TestSplitChatMCPArgs(t *testing.T) {
	args, err := splitChatMCPArgs(`add x --env 'A=b c' -- "my cmd"  y`)
	if err != nil || strings.Join(args, "|") != "add|x|--env|A=b c|--|my cmd|y" {
		t.Fatalf("%q %v", args, err)
	}
	if _, err = splitChatMCPArgs(`add "x`); err == nil {
		t.Fatal("unclosed quote accepted")
	}
}
