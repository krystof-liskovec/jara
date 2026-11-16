package jara

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
	"github.com/klauspost/compress/zstd"
)

// Native session files are append-only logs that grow to tens of megabytes,
// and a running device checkpoints every few seconds. A device therefore
// remembers how much of each file the host already stores and sends only the
// bytes appended since (Base > 0). The host stores session files raw, beside
// the conversation record, and appends a delta only when its copy ends like
// the device's did (Tail); otherwise it asks for the whole file again.
// Checkpoints travel zstd-compressed.

// runnerTailSize is how much of a file's end identifies it for a delta.
const runnerTailSize = 4096

const runnerResendMessage = "checkpoint needs whole conversation files"

var errRunnerResend = errors.New(runnerResendMessage)

// runnerSentFile is what the host stores of one local session file.
type runnerSentFile struct {
	Size int64  `json:"size"`
	Tail string `json:"tail"`
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

// readNativeFile reads a session file, from the end of what the host stores
// when the file only grew since. It returns the file's new sent state.
func readNativeFile(path string, size int64, sent map[string]runnerSentFile, file *runnerNativeFile) (runnerSentFile, error) {
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
	file.Base = base
	file.setData(data)
	tail, err := fileTail(path, size)
	return runnerSentFile{Size: size, Tail: tail}, err
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

// storeNativeFiles writes a checkpointed conversation's session files beside
// its record, appending deltas, and leaves the bundle without file contents.
func storeNativeFiles(paths runnerPaths, bundle *runnerBundle) error {
	kept := bundle.Files[:0]
	for _, file := range bundle.Files {
		if file.Agent != protocol.AgentClaude && file.Agent != protocol.AgentCodex || !portable.ValidBackendID(file.BackendID) {
			continue
		}
		dest := paths.storedNativePath(bundle.ThreadID, file)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := writeStoredNative(dest, file); err != nil {
			return err
		}
		file.Data, file.Text, file.Base, file.Tail = nil, "", 0, ""
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
	info, err := os.Stat(dest)
	if err != nil || info.Size() != file.Base {
		return errRunnerResend
	}
	if tail, tailErr := fileTail(dest, file.Base); tailErr != nil || tail != file.Tail {
		return errRunnerResend
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

// zstdMagic starts every zstd frame; plain JSON never does.
var zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}

// compressedJSON encodes v as zstd-compressed JSON.
func compressedJSON(v any) ([]byte, error) {
	var out bytes.Buffer
	encoder, err := zstd.NewWriter(&out, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	if err = json.NewEncoder(encoder).Encode(v); err != nil {
		encoder.Close()
		return nil, err
	}
	if err = encoder.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// decodeMaybeCompressed decodes JSON that may be zstd-compressed, reading at
// most limit decompressed bytes.
func decodeMaybeCompressed(r io.Reader, limit int64, v any) error {
	buffered := bufio.NewReader(r)
	if head, _ := buffered.Peek(len(zstdMagic)); bytes.Equal(head, zstdMagic) {
		decoder, err := zstd.NewReader(buffered, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return err
		}
		defer decoder.Close()
		return json.NewDecoder(io.LimitReader(decoder, limit)).Decode(v)
	}
	return json.NewDecoder(io.LimitReader(buffered, limit)).Decode(v)
}

// rpcCompressed is rpc for requests and answers that may be compressed.
func rpcCompressed(r Remote, q Request, body any, v any) error {
	var input io.Reader
	if body != nil {
		data, err := compressedJSON(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(data)
	}
	var out bytes.Buffer
	if err := rpcStream(r, q, input, &out); err != nil {
		return err
	}
	if v == nil {
		return nil
	}
	if err := decodeMaybeCompressed(&out, maxTransfer*4, v); err != nil {
		return fmt.Errorf("invalid Jara response: %w", err)
	}
	return nil
}
