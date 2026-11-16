package jara

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"krystof.liskovec/jara/internal/portable"
	"krystof.liskovec/jara/internal/protocol"
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
	return tailHash(data), nil
}

// tailHash hashes the runnerTailSize bytes at the end of data.
func tailHash(data []byte) string {
	sum := sha256.Sum256(data[max(0, len(data)-runnerTailSize):])
	return hex.EncodeToString(sum[:16])
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

// storedCopy is the host's copy of a session file as a sender sees it. A
// copy stored from another checkout has that checkout's path inside; a delta
// from this sender continues the copy converted to the sender's path.
type storedCopy struct {
	path      string
	size      int64
	exists    bool
	converted []byte // the converted copy, when conversion changed it
}

// openStoredCopy reads the copy at path, stored from storedRoot, for a sender
// whose checkout is at root.
func openStoredCopy(path, storedRoot, root string) storedCopy {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return storedCopy{path: path}
	}
	stored := storedCopy{path: path, size: info.Size(), exists: true}
	if storedRoot != "" && root != "" && storedRoot != root {
		if data, readErr := os.ReadFile(path); readErr == nil {
			if converted := portable.RewritePath(data, storedRoot, root); !bytes.Equal(converted, data) {
				stored.converted, stored.size = converted, int64(len(converted))
			}
		}
	}
	return stored
}

func (c storedCopy) tail(size int64) (string, error) {
	if c.converted != nil {
		return tailHash(c.converted[:size]), nil
	}
	return fileTail(c.path, size)
}

func (c storedCopy) hashPrefix(h hash.Hash, size int64) error {
	if c.converted != nil {
		h.Write(c.converted[:size])
		return nil
	}
	return hashFilePrefix(h, c.path, size)
}

// storedBundleRoot is the checkout a stored conversation was checkpointed
// from, or "" when none is stored.
func storedBundleRoot(paths runnerPaths, thread string) string {
	var stored struct {
		Root string `json:"root"`
	}
	if data, err := os.ReadFile(paths.thread(thread)); err == nil {
		_ = json.Unmarshal(data, &stored)
	}
	return stored.Root
}

// checkNativeFiles verifies that every session file of a checkpoint applies
// to the host's copies, so a checkpoint is stored completely or not at all.
func checkNativeFiles(paths runnerPaths, bundles []runnerBundle) error {
	var resync []runnerResync
	for _, bundle := range bundles {
		storedRoot := storedBundleRoot(paths, bundle.ThreadID)
		for _, file := range bundle.Files {
			if !storableNative(file) {
				continue
			}
			stored := storedCopy{}
			if file.Base > 0 {
				stored = openStoredCopy(paths.storedNativePath(bundle.ThreadID, file), storedRoot, bundle.Root)
			}
			continues, sumMatches := checkStoredNative(stored, file)
			if continues && sumMatches {
				continue
			}
			have := runnerResync{ThreadID: bundle.ThreadID, Agent: file.Agent, BackendID: file.BackendID}
			// A copy that ends right but hashes wrong is damaged inside: only
			// the whole file repairs it.
			if !continues && stored.exists {
				have.Size = stored.size
				have.Tail, _ = stored.tail(stored.size)
			}
			resync = append(resync, have)
		}
	}
	if len(resync) > 0 {
		return &runnerResyncError{files: resync}
	}
	return nil
}

// checkStoredNative reports whether file continues the host's copy, and
// whether the result matches the file's checksum, if it has one.
func checkStoredNative(stored storedCopy, file runnerNativeFile) (continues, sumMatches bool) {
	if file.Base > 0 {
		if !stored.exists || stored.size != file.Base {
			return false, true
		}
		if tail, err := stored.tail(file.Base); err != nil || tail != file.Tail {
			return false, true
		}
	}
	if file.Sum == "" {
		return true, true
	}
	h := sha256.New()
	if file.Base > 0 && stored.hashPrefix(h, file.Base) != nil {
		return true, false
	}
	h.Write(file.bytes())
	return true, hex.EncodeToString(h.Sum(nil)) == file.Sum
}

// storeNativeFiles writes a checked conversation's session files beside its
// record, appending deltas, and leaves the bundle without file contents. The
// caller stores the record afterwards, so the old record still names the
// checkout the copies were stored from.
func storeNativeFiles(paths runnerPaths, bundle *runnerBundle) error {
	storedRoot := storedBundleRoot(paths, bundle.ThreadID)
	kept := bundle.Files[:0]
	for _, file := range bundle.Files {
		if !storableNative(file) {
			continue
		}
		dest := paths.storedNativePath(bundle.ThreadID, file)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := writeStoredNative(dest, storedRoot, bundle.Root, file); err != nil {
			return err
		}
		file.Data, file.Text, file.Base, file.Tail, file.Sum = nil, "", 0, "", ""
		kept = append(kept, file)
	}
	bundle.Files = kept
	return nil
}

func writeStoredNative(dest, storedRoot, root string, file runnerNativeFile) error {
	data := file.bytes()
	if file.Base > 0 {
		stored := openStoredCopy(dest, storedRoot, root)
		if stored.converted == nil {
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
		// Stored from another checkout: keep it in this sender's form, which
		// the delta continues.
		data = append(stored.converted[:file.Base:file.Base], data...)
	}
	temp := dest + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	return os.Rename(temp, dest)
}

// loadNativeFiles fills a stored bundle's session files back in, as deltas
// against what the receiver has where it can. Bundles stored before files
// were kept separately carry their contents inline.
func loadNativeFiles(paths runnerPaths, bundle *runnerBundle, haves []runnerHave) {
	kept := bundle.Files[:0]
	for _, file := range bundle.Files {
		if file.size() == 0 {
			path := paths.storedNativePath(bundle.ThreadID, file)
			if storedDelta(path, haves, bundle.ThreadID, bundle.Root, &file) {
				kept = append(kept, file)
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			file.setData(data)
		}
		kept = append(kept, file)
	}
	bundle.Files = kept
}
