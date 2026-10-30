package jara

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClipboardKeyStream(t *testing.T) {
	// Includes paste-like bytes within a text paste, fragmented escape sequences,
	// key releases, Ctrl+C, tmux detach, arrow keys and terminal color replies.
	input := "draft\x03\x02d\x1b[A\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[200~literal\x16\x1b[118;5u\x1b[201~\x16\x1b[118;5u\x1b[118;5:3u"
	want := "draft\x03\x02d\x1b[A\x1b]11;rgb:ffff/ffff/ffff\x07\x1b[200~literal\x16\x1b[118;5u\x1b[201~<image><image>"
	for size := 1; size <= len(input); size++ {
		var out bytes.Buffer
		keys := clipboardKeys{write: func(b []byte) error { _, err := out.Write(b); return err }, image: func() error { out.WriteString("<image>"); return nil }}
		for start := 0; start < len(input); start += size {
			if err := keys.feed([]byte(input[start:min(start+size, len(input))])); err != nil {
				t.Fatal(err)
			}
		}
		if err := keys.flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("chunk %d: %q", size, out.String())
		}
	}
}

func TestClipboardUpload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("JARA_DATA", root)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	var response bytes.Buffer
	if err := receiveClipboardImage(bytes.NewReader(data.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	path, err := decodeClipboardPath(response.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, filepath.Join(root, "attachments")+string(os.PathSeparator)) {
		t.Fatal(path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data.Bytes()) {
		t.Fatalf("image not preserved: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	for _, bad := range [][]byte{[]byte("not an image"), bytes.Repeat([]byte("x"), maxClipboardImage+1)} {
		if err := receiveClipboardImage(bytes.NewReader(bad), &response); err == nil {
			t.Fatal("accepted invalid/oversized image")
		}
	}
	for _, path := range []string{"relative.png", "/tmp/escape\x1b[201~\r.png"} {
		data, _ := json.Marshal(path)
		if _, err := decodeClipboardPath(data); err == nil {
			t.Fatal("accepted unsafe path")
		}
	}
}

func TestImagePasteDoesNotSubmit(t *testing.T) {
	got := imagePaste("/home/agent/attachments/image.png")
	if got != "\x1b[200~/home/agent/attachments/image.png\x1b[201~" || strings.ContainsAny(got, "\r\n") {
		t.Fatal(got)
	}
}
