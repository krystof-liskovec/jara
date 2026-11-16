package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// Native session files are append-only logs that grow to tens of megabytes,
// and a running device checkpoints every few seconds. A device therefore
// remembers how much of each file the host already stores and sends only the
// bytes appended since (Base > 0). The host stores session files raw, beside
// the conversation record.
//
// Before writing anything, the host checks every file of a checkpoint:
//
//   - A delta must continue the host's copy: same length as Base, and the
//     runnerTailSize bytes before Base hash to Tail.
//   - When the turn has ended, the device also sends the SHA-256 of each whole
//     file (Sum); the host's copy plus the delta must hash to it.
//
// When a file does not match, the host stores nothing and answers with what it
// has of each such file (runnerResync). The device continues from there when
// its own file has the same bytes at that point, as after a checkpoint whose
// answer was lost or whose append was cut short, and sends the whole file
// only when the copies really differ.

// runnerTailSize is how much of a file's end identifies it for a delta.
const runnerTailSize = 4096

// runnerResendMessage is how hosts before repair refused a delta.
const runnerResendMessage = "checkpoint needs whole conversation files"

// runnerSentFile is what the host stores of one local session file.
type runnerSentFile struct {
	Size int64  `json:"size"`
	Tail string `json:"tail"`
}

// runnerResync tells a device what the host has of one session file that
// did not match a checkpoint. Size 0 asks for the whole file.
type runnerResync struct {
	ThreadID  string `json:"thread_id"`
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	Size      int64  `json:"size,omitempty"`
	Tail      string `json:"tail,omitempty"`
}

type runnerResyncError struct{ files []runnerResync }

func (e *runnerResyncError) Error() string {
	return fmt.Sprintf("%d conversation file(s) differ from the host's copy", len(e.files))
}

// fileTail hashes the runnerTailSize bytes before offset size.
func fileTail(path string, size int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	start := max(0, size-runnerTailSize)
	data := make([]byte, size-start)
	if _, err = file.ReadAt(data, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]), nil
}

// hashFilePrefix feeds the first size bytes of path into h.
func hashFilePrefix(h hash.Hash, path string, size int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	copied, err := io.Copy(h, io.LimitReader(file, size))
	if err == nil && copied != size {
		err = fmt.Errorf("%s is shorter than %d bytes", path, size)
	}
	return err
}

// readNativeFile reads a session file, from the end of what the host stores
// when the file only grew since, and with verify the whole file's checksum.
// It returns the file's sent state once the host accepts it.
func readNativeFile(path string, size int64, sent map[string]runnerSentFile, verify bool, file *runnerNativeFile) (runnerSentFile, error) {
	base := int64(0)
	if previous, ok := sent[path]; ok && previous.Size > 0 && previous.Size <= size {
		if tail, err := fileTail(path, previous.Size); err == nil && tail == previous.Tail {
			base, file.Tail = previous.Size, previous.Tail
		}
	}
	handle, err := os.Open(path)
	if err != nil {
		return runnerSentFile{}, err
	}
	defer handle.Close()
	data := make([]byte, size-base)
	if _, err = handle.ReadAt(data, base); err != nil && !errors.Is(err, io.EOF) {
		return runnerSentFile{}, err
	}
	file.Base, file.local = base, path
	file.setData(data)
	if verify {
		h := sha256.New()
		if err = hashFilePrefix(h, path, size); err != nil {
			return runnerSentFile{}, err
		}
		file.Sum = hex.EncodeToString(h.Sum(nil))
	}
	tail, err := fileTail(path, size)
	return runnerSentFile{Size: size, Tail: tail}, err
}

// resumeFrom reports where to continue a file the host has size bytes of,
// ending in tail: there when this file has the same bytes, otherwise from the
// start.
func resumeFrom(path string, size int64, tail string) (runnerSentFile, bool) {
	info, err := os.Stat(path)
	if size == 0 || err != nil || info.Size() < size {
		return runnerSentFile{}, false
	}
	if local, err := fileTail(path, size); err != nil || local != tail {
		return runnerSentFile{}, false
	}
	return runnerSentFile{Size: size, Tail: tail}, true
}

// setData stores data as text when it is UTF-8, which compresses far better
// than JSON's base64 for binary data.
func (f *runnerNativeFile) setData(data []byte) {
	f.Data, f.Text = nil, ""
	if utf8.Valid(data) {
		f.Text = string(data)
		return
	}
	f.Data = data
}

func (f runnerNativeFile) bytes() []byte {
	if f.Text != "" {
		return []byte(f.Text)
	}
	return f.Data
}

func (f runnerNativeFile) size() int { return len(f.Data) + len(f.Text) }

// storedNativePath is where the host keeps one session file of a conversation.
func (p runnerPaths) storedNativePath(thread string, file runnerNativeFile) string {
	return filepath.Join(p.threads, thread+".files", file.Agent+"-"+file.BackendID)
}

func storableNative(file runnerNativeFile) bool {
	return (file.Agent == protocol.AgentClaude || file.Agent == protocol.AgentCodex) && portable.ValidBackendID(file.BackendID)
}

// checkNativeFiles verifies that every session file of a checkpoint applies
// to the host's copies, so a checkpoint is stored completely or not at all.
func checkNativeFiles(paths runnerPaths, bundles []runnerBundle) error {
	var resync []runnerResync
	for _, bundle := range bundles {
		for _, file := range bundle.Files {
			if !storableNative(file) {
				continue
			}
			dest := paths.storedNativePath(bundle.ThreadID, file)
			continues, sumMatches := checkStoredNative(dest, file)
			if continues && sumMatches {
				continue
			}
			have := runnerResync{ThreadID: bundle.ThreadID, Agent: file.Agent, BackendID: file.BackendID}
			// A copy that ends right but hashes wrong is damaged inside: only
			// the whole file repairs it.
			if info, err := os.Stat(dest); !continues && err == nil && info.Mode().IsRegular() {
				have.Size = info.Size()
				have.Tail, _ = fileTail(dest, have.Size)
			}
			resync = append(resync, have)
		}
	}
	if len(resync) > 0 {
		return &runnerResyncError{files: resync}
	}
	return nil
}

// checkStoredNative reports whether file continues the host's copy at dest,
// and whether the result matches the file's checksum, if it has one.
func checkStoredNative(dest string, file runnerNativeFile) (continues, sumMatches bool) {
	if file.Base > 0 {
		info, err := os.Stat(dest)
		if err != nil || info.Size() != file.Base {
			return false, true
		}
		if tail, err := fileTail(dest, file.Base); err != nil || tail != file.Tail {
			return false, true
		}
	}
	if file.Sum == "" {
		return true, true
	}
	h := sha256.New()
	if file.Base > 0 && hashFilePrefix(h, dest, file.Base) != nil {
		return true, false
	}
	h.Write(file.bytes())
	return true, hex.EncodeToString(h.Sum(nil)) == file.Sum
}

// storeNativeFiles writes a checked conversation's session files beside its
// record, appending deltas, and leaves the bundle without file contents.
func storeNativeFiles(paths runnerPaths, bundle *runnerBundle) error {
	kept := bundle.Files[:0]
	for _, file := range bundle.Files {
		if !storableNative(file) {
			continue
		}
		dest := paths.storedNativePath(bundle.ThreadID, file)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := writeStoredNative(dest, file); err != nil {
			return err
		}
		file.Data, file.Text, file.Base, file.Tail, file.Sum = nil, "", 0, "", ""
		kept = append(kept, file)
	}
	bundle.Files = kept
	return nil
}

func writeStoredNative(dest string, file runnerNativeFile) error {
	data := file.bytes()
	if file.Base == 0 {
		temp := dest + ".tmp"
		if err := os.WriteFile(temp, data, 0600); err != nil {
			return err
		}
		return os.Rename(temp, dest)
	}
	handle, err := os.OpenFile(dest, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err = handle.Write(data); err != nil {
		handle.Close()
		return err
	}
	return handle.Close()
}

// loadNativeFiles fills a stored bundle's session files back in. Bundles
// stored before files were kept separately carry their contents inline.
func loadNativeFiles(paths runnerPaths, bundle *runnerBundle) {
	kept := bundle.Files[:0]
	for _, file := range bundle.Files {
		if file.size() == 0 {
			data, err := os.ReadFile(paths.storedNativePath(bundle.ThreadID, file))
			if err != nil {
				continue
			}
			file.setData(data)
		}
		kept = append(kept, file)
	}
	bundle.Files = kept
}
