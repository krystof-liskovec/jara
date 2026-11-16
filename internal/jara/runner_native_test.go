package jara

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointSendsOnlyAppendedSessionBytes(t *testing.T) {
	base := t.TempDir()
	t.Setenv("JARA_DATA", filepath.Join(base, "d"))
	paths, err := runnerPathSet("w-delta")
	if err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(base, "session.jsonl")
	first := strings.Repeat(`{"type":"message","text":"hello"}`+"\n", 300)
	if err = os.WriteFile(session, []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	send := func(sent map[string]runnerSentFile) (runnerNativeFile, map[string]runnerSentFile, error) {
		file := runnerNativeFile{Agent: "claude", BackendID: "11111111-2222-3333-4444-555555555555"}
		info, _ := os.Stat(session)
		state, err := readNativeFile(session, info.Size(), sent, &file)
		if err != nil {
			t.Fatal(err)
		}
		bundle := runnerBundle{ThreadID: "th-1", Files: []runnerNativeFile{file}}
		if err = os.MkdirAll(paths.threads, 0700); err == nil {
			err = storeNativeFiles(paths, &bundle)
		}
		return file, map[string]runnerSentFile{session: state}, err
	}
	file, sent, err := send(nil)
	if err != nil || file.Base != 0 || file.Text != first {
		t.Fatalf("first checkpoint must carry the whole file: base %d, %d bytes, %v", file.Base, file.size(), err)
	}
	appended := `{"type":"message","text":"more"}` + "\n"
	handle, _ := os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = handle.WriteString(appended)
	handle.Close()
	file, sent, err = send(sent)
	if err != nil || file.Base != int64(len(first)) || file.Text != appended {
		t.Fatalf("delta = base %d %q, %v", file.Base, file.Text, err)
	}
	bundle := runnerBundle{ThreadID: "th-1", Files: []runnerNativeFile{{Agent: "claude", BackendID: file.BackendID}}}
	loadNativeFiles(paths, &bundle)
	if got := string(bundle.Files[0].bytes()); got != first+appended {
		t.Fatalf("host copy has %d bytes, want %d", len(got), len(first+appended))
	}

	// The host's copy changed (another machine wrote it): the delta is refused.
	stored := paths.storedNativePath("th-1", file)
	if err = os.WriteFile(stored, []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	handle, _ = os.OpenFile(session, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = handle.WriteString(appended)
	handle.Close()
	if _, _, err = send(sent); !errors.Is(err, errRunnerResend) {
		t.Fatalf("mismatched host copy accepted a delta: %v", err)
	}
	// A rewritten local file is sent whole.
	if err = os.WriteFile(session, []byte("rewritten\n"+first+appended), 0600); err != nil {
		t.Fatal(err)
	}
	if file, _, err = send(sent); err != nil || file.Base != 0 {
		t.Fatalf("rewritten file sent as a delta: base %d, %v", file.Base, err)
	}
}

func TestCompressedCheckpointRoundTrip(t *testing.T) {
	checkpoint := runnerCheckpoint{Current: "th-1", Threads: []runnerBundle{{ThreadID: "th-1",
		Files: []runnerNativeFile{{Agent: "codex", Text: strings.Repeat("log line\n", 5000)}}}}}
	data, err := compressedJSON(checkpoint)
	if err != nil || !bytes.HasPrefix(data, zstdMagic) || len(data) > 2000 {
		t.Fatalf("compressed checkpoint: %d bytes, %v", len(data), err)
	}
	var decoded runnerCheckpoint
	if err = decodeMaybeCompressed(bytes.NewReader(data), maxTransfer, &decoded); err != nil || decoded.Threads[0].Files[0].Text != checkpoint.Threads[0].Files[0].Text {
		t.Fatalf("round trip failed: %v", err)
	}
	if err = decodeMaybeCompressed(strings.NewReader(`{"current":"plain"}`), maxTransfer, &decoded); err != nil || decoded.Current != "plain" {
		t.Fatalf("plain JSON from an older Jara: %v", err)
	}
}
