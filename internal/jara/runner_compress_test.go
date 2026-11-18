package jara

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// serveThrough runs an echo handler under serveCompressedRPC with stdin
// holding input, and returns what it wrote to stdout.
func serveThrough(t *testing.T, q Request, input []byte) (handlerSaw, output []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, input, 0600); err != nil {
		t.Fatal(err)
	}
	stdin, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	realStdout := os.Stdout
	os.Stdout = writer
	captured := make(chan []byte)
	go func() { data, _ := io.ReadAll(reader); captured <- data }()
	err = serveCompressedRPC(stdin, q, func() error {
		handlerSaw, _ = io.ReadAll(os.Stdin)
		_, writeErr := os.Stdout.Write(bytes.Repeat([]byte(`{"file":"same text"}`), 500))
		return writeErr
	})
	os.Stdout = realStdout
	writer.Close()
	output = <-captured
	if err != nil {
		t.Fatal(err)
	}
	return handlerSaw, output
}

func TestRPCCompressionIsTransparentAndCompatible(t *testing.T) {
	answer := bytes.Repeat([]byte(`{"file":"same text"}`), 500)
	upload := []byte(strings.Repeat("a line of a source file\n", 400))

	// A new client: compressed upload, compressed answer.
	var compressed bytes.Buffer
	encoder, _ := zstd.NewWriter(&compressed)
	encoder.Write(upload)
	encoder.Close()
	saw, output := serveThrough(t, Request{Op: "apply", Compress: true}, compressed.Bytes())
	if !bytes.Equal(saw, upload) {
		t.Fatalf("handler read %d bytes, want the %d-byte upload", len(saw), len(upload))
	}
	if !bytes.HasPrefix(output, zstdMagic) || len(output) > len(answer)/10 {
		t.Fatalf("answer not compressed: %d bytes", len(output))
	}
	remote := Remote{Host: "new-host"}
	plain, err := decompressAnswer(remote, output)
	if err != nil || !bytes.Equal(plain, answer) {
		t.Fatalf("answer did not round-trip: %v", err)
	}
	// Having answered compressed, the host gets compressed uploads.
	sent, _ := io.ReadAll(compressRequestInput(remote, bytes.NewReader(upload)))
	if !bytes.HasPrefix(sent, zstdMagic) {
		t.Fatal("upload to a host known to compress was sent plain")
	}

	// An older client: plain upload, plain answer.
	saw, output = serveThrough(t, Request{Op: "apply"}, upload)
	if !bytes.Equal(saw, upload) || !bytes.Equal(output, answer) {
		t.Fatal("a client that does not compress got a changed exchange")
	}
	// An older host answers plain; the client then never compresses for it.
	old := Remote{Host: "old-host"}
	if plain, err = decompressAnswer(old, answer); err != nil || !bytes.Equal(plain, answer) {
		t.Fatalf("plain answer: %v", err)
	}
	if sent, _ = io.ReadAll(compressRequestInput(old, bytes.NewReader(upload))); !bytes.Equal(sent, upload) {
		t.Fatal("an older host would receive zstd")
	}
}

func TestEverySSHSessionIsCompressed(t *testing.T) {
	for _, tty := range []bool{false, true} {
		args := strings.Join(sshArgs(Remote{Host: "work.example"}, tty), " ")
		if strings.Count(args, "Compression=yes") != 1 {
			t.Errorf("tty %v: %s", tty, args)
		}
	}
}
