package jara

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCodexWithoutSharedDaemon(t *testing.T) {
	supported := true
	previous := codexSupportsNoDaemon
	codexSupportsNoDaemon = func(string) bool { return supported }
	defer func() { codexSupportsNoDaemon = previous }()

	got := codexWithoutSharedDaemon([]string{codexBinary, "resume", "thread-1", "--model", "x"})
	want := []string{codexBinary, "--no-daemon", "resume", "thread-1", "--model", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got = codexWithoutSharedDaemon(want); !reflect.DeepEqual(got, want) {
		t.Fatalf("flag duplicated: %q", got)
	}
	claude := []string{claudeBinary, "--resume"}
	if got = codexWithoutSharedDaemon(claude); !reflect.DeepEqual(got, claude) {
		t.Fatalf("Claude command changed: %q", got)
	}
	supported = false
	old := []string{codexBinary, "resume"}
	if got = codexWithoutSharedDaemon(old); !reflect.DeepEqual(got, old) {
		t.Fatalf("unsupported Codex received the flag: %q", got)
	}
}

func TestDefaultChatInterface(t *testing.T) {
	for action, want := range map[string]string{"open": "jara", "new": "jara", "resume": "jara", "sessions": "jara", "run": "codex", "handoff": "codex", "sync": "codex"} {
		if got := defaultChatInterface(action); got != want {
			t.Errorf("%s: got %q, want %q", action, got, want)
		}
	}
}

func TestResumeWriterConflictExplainsRecovery(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	var output bytes.Buffer
	u := &chatUI{out: &output, thread: "01a1", items: map[string]json.RawMessage{}}
	message := appMessage{Error: &struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}{Message: "thread 01a1 already has an active writer"}}
	if !recoverChatRequestError(u, chatPending{method: "thread/resume"}, message) {
		t.Fatal("resume failure was not recovered")
	}
	if !strings.Contains(output.String(), "codex app-server daemon stop") {
		t.Fatalf("missing recovery hint: %q", output.String())
	}
}
