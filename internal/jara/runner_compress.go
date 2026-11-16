package jara

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
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
