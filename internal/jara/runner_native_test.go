package jara

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nativeFixture is one device session file and the host's stored copy.
type nativeFixture struct {
	t       *testing.T
	paths   runnerPaths
	session string
	sent    map[string]runnerSentFile
}

func newNativeFixture(t *testing.T) *nativeFixture {
	base := t.TempDir()
	t.Setenv("JARA_DATA", filepath.Join(base, "d"))
	paths, err := runnerPathSet("w-delta")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(paths.threads, 0700); err != nil {
		t.Fatal(err)
	}
	return &nativeFixture{t: t, paths: paths, session: filepath.Join(base, "session.jsonl"), sent: map[string]runnerSentFile{}}
}

func (f *nativeFixture) append(text string) {
	handle, err := os.OpenFile(f.session, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	_, _ = handle.WriteString(text)
	handle.Close()
}

// checkpoint sends the file as a device does, repairing as the host asks,
// and returns the bytes sent in each attempt.
func (f *nativeFixture) checkpoint(verify bool) []int {
	var sizes []int
	for attempt := 0; attempt < 3; attempt++ {
		file := runnerNativeFile{Agent: "claude", BackendID: "11111111-2222-3333-4444-555555555555"}
		info, _ := os.Stat(f.session)
		state, err := readNativeFile(f.session, info.Size(), f.sent, verify, &file)
		if err != nil {
			f.t.Fatal(err)
		}
		sizes = append(sizes, file.size())
		checkpoint := runnerCheckpoint{Threads: []runnerBundle{{ThreadID: "th-1", Files: []runnerNativeFile{file}}}}
		err = checkNativeFiles(f.paths, checkpoint.Threads)
		var resync *runnerResyncError
		if errors.As(err, &resync) {
			repairSent(f.sent, checkpoint, resync.files)
			continue
		}
		if err == nil {
			err = storeNativeFiles(f.paths, &checkpoint.Threads[0])
		}
		if err != nil {
			f.t.Fatal(err)
		}
		f.sent[f.session] = state
		return sizes
	}
	f.t.Fatalf("checkpoint did not converge: %v", sizes)
	return nil
}

func (f *nativeFixture) stored() string {
	data, _ := os.ReadFile(filepath.Join(f.paths.threads, "th-1.files", "claude-11111111-2222-3333-4444-555555555555"))
	return string(data)
}

func (f *nativeFixture) expectHostCopy() {
	local, _ := os.ReadFile(f.session)
	if f.stored() != string(local) {
		f.t.Fatalf("host copy has %d bytes, device %d", len(f.stored()), len(local))
	}
}

func TestCheckpointSendsOnlyAppendedSessionBytes(t *testing.T) {
	f := newNativeFixture(t)
	line := `{"type":"message","text":"hello"}` + "\n"
	f.append(strings.Repeat(line, 300))
	if sizes := f.checkpoint(false); len(sizes) != 1 || sizes[0] != 300*len(line) {
		t.Fatalf("first checkpoint must carry the whole file once: %v", sizes)
	}
	f.append(line)
	if sizes := f.checkpoint(true); len(sizes) != 1 || sizes[0] != len(line) {
		t.Fatalf("a verified delta: %v", sizes)
	}
	f.expectHostCopy()
}

func TestCheckpointRepairsFromWhatTheHostHas(t *testing.T) {
	f := newNativeFixture(t)
	line := `{"type":"message","text":"a line of the conversation"}` + "\n"
	f.append(strings.Repeat(line, 200))
	f.checkpoint(false)

	// The host stored a checkpoint but its answer was lost: the device sends
	// from its older offset, and continues from the host's end instead.
	lost := f.sent[f.session]
	f.append(line)
	f.checkpoint(false)
	f.sent[f.session] = lost
	f.append(line)
	if sizes := f.checkpoint(false); len(sizes) != 2 || sizes[1] != len(line) {
		t.Fatalf("after a lost answer: %v", sizes)
	}
	f.expectHostCopy()

	// The connection dropped in the middle of an append.
	f.append(strings.Repeat(line, 3))
	stored := f.stored()
	if err := os.WriteFile(filepath.Join(f.paths.threads, "th-1.files", "claude-11111111-2222-3333-4444-555555555555"),
		[]byte(stored+strings.Repeat(line, 3)[:len(line)+7]), 0600); err != nil {
		t.Fatal(err)
	}
	if sizes := f.checkpoint(false); len(sizes) != 2 || sizes[1] != 2*len(line)-7 {
		t.Fatalf("after a cut-short append: %v", sizes)
	}
	f.expectHostCopy()

	// Damage inside the host's copy is invisible at its end; the checksum at
	// the end of a turn finds it and the whole file repairs it.
	damaged := []byte(f.stored())
	damaged[10] = 'X'
	if err := os.WriteFile(filepath.Join(f.paths.threads, "th-1.files", "claude-11111111-2222-3333-4444-555555555555"), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	f.append(line)
	if sizes := f.checkpoint(false); len(sizes) != 1 {
		t.Fatalf("an unverified delta: %v", sizes)
	}
	f.append(line)
	local, _ := os.ReadFile(f.session)
	if sizes := f.checkpoint(true); len(sizes) != 2 || sizes[1] != len(local) {
		t.Fatalf("damaged host copy: %v", sizes)
	}
	f.expectHostCopy()

	// A copy written by another machine shares nothing: the whole file goes.
	if err := os.WriteFile(filepath.Join(f.paths.threads, "th-1.files", "claude-11111111-2222-3333-4444-555555555555"), []byte("other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f.append(line)
	local, _ = os.ReadFile(f.session)
	if sizes := f.checkpoint(false); len(sizes) != 2 || sizes[1] != len(local) {
		t.Fatalf("diverged host copy: %v", sizes)
	}
	f.expectHostCopy()
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
