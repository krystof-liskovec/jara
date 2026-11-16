package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/term"
)

// Checkpoints travel as zstd-compressed JSON. Readers accept plain JSON too,
// from a Jara that does not compress yet.

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
	q.Compress = true
	if err := rpcStream(r, q, input, &out); err != nil {
		return err
	}
	if bytes.HasPrefix(out.Bytes(), zstdMagic) {
		zstdHosts.Store(remoteKey(r), true)
	}
	if v == nil {
		return nil
	}
	if err := decodeMaybeCompressed(&out, maxTransfer*4, v); err != nil {
		return fmt.Errorf("invalid Jara response: %w", err)
	}
	return nil
}

// Every RPC answer and upload is zstd-compressed between Jara versions that
// support it. A client asks for compressed answers (Request.Compress), which
// an older host ignores; it recognises a compressed answer by its first
// bytes. It compresses what it sends only to a host that already answered
// compressed in this process, so an older host never receives zstd.

var zstdHosts sync.Map // remote → true once it answered compressed

func remoteKey(r Remote) string { return r.User + "@" + r.Host + ":" + strconv.Itoa(r.Port) }

// compressRequestInput compresses input for a host known to understand it.
func compressRequestInput(r Remote, input io.Reader) io.Reader {
	if input == nil {
		return nil
	}
	if _, ok := zstdHosts.Load(remoteKey(r)); !ok {
		return input
	}
	reader, writer := io.Pipe()
	go func() {
		encoder, err := zstd.NewWriter(writer, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err == nil {
			if _, err = io.Copy(encoder, input); err == nil {
				err = encoder.Close()
			} else {
				encoder.Close()
			}
		}
		writer.CloseWithError(err)
	}()
	return reader
}

// decompressAnswer returns an RPC answer's plain bytes and remembers that
// the host compresses.
func decompressAnswer(r Remote, data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, zstdMagic) {
		return data, nil
	}
	zstdHosts.Store(remoteKey(r), true)
	decoder, err := zstd.NewReader(bytes.NewReader(data), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return io.ReadAll(io.LimitReader(decoder, maxTransfer*4))
}

// serveCompressedRPC runs an RPC on the host with its input (stdin, or none
// when nil) decompressed when the client compressed it, and its output
// compressed when the client asked. Handlers keep reading os.Stdin and
// writing os.Stdout.
func serveCompressedRPC(stdin *os.File, q Request, serve func() error) error {
	originalIn, stdout := os.Stdin, os.Stdout
	defer func() { os.Stdin, os.Stdout = originalIn, stdout }()
	var head []byte
	var buffered *bufio.Reader
	if stdin != nil && !term.IsTerminal(int(stdin.Fd())) { // a person typing sends no zstd
		buffered = bufio.NewReader(stdin)
		head, _ = buffered.Peek(len(zstdMagic))
	}
	if len(head) > 0 {
		var source io.Reader = buffered
		if bytes.Equal(head, zstdMagic) {
			decoder, err := zstd.NewReader(buffered, zstd.WithDecoderConcurrency(1))
			if err != nil {
				return err
			}
			defer decoder.Close()
			source = decoder
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		defer reader.Close()
		go func() {
			_, _ = io.Copy(writer, source)
			writer.Close()
		}()
		os.Stdin = reader
	}
	if !q.Compress {
		return serve()
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		encoder, err := zstd.NewWriter(stdout, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err == nil {
			_, err = io.Copy(encoder, reader)
			if closeErr := encoder.Close(); err == nil {
				err = closeErr
			}
		}
		reader.Close()
		done <- err
	}()
	os.Stdout = writer
	serveErr := serve()
	writer.Close()
	if err = <-done; serveErr == nil {
		serveErr = err
	}
	return serveErr
}
