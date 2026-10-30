package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func clipboardImageFormat(data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxClipboardImage {
		return "", fmt.Errorf("image must contain at most 20 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return "", fmt.Errorf("clipboard must contain a PNG or JPEG image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", fmt.Errorf("clipboard image exceeds 40 million pixels")
	}
	return format, nil
}

func receiveClipboardImage(input io.Reader, output io.Writer) error {
	gate, err := lockLifecycle()
	if err != nil {
		return err
	}
	defer gate()
	if err = admitWorkspace(); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, maxClipboardImage+1))
	if err != nil {
		return err
	}
	format, err := clipboardImageFormat(data)
	if err != nil {
		return err
	}
	root := filepath.Join(dataRoot(), "attachments")
	if err = os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(root, "clipboard-")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "image."+format)
	if err = os.WriteFile(path, data, 0600); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err = json.NewEncoder(output).Encode(path); err != nil {
		os.RemoveAll(dir)
	}
	return err
}

func decodeClipboardPath(data []byte) (string, error) {
	var path string
	if err := json.Unmarshal(data, &path); err != nil {
		return "", fmt.Errorf("invalid image upload response; update the remote Jara installation")
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\x1b\r\n") {
		return "", fmt.Errorf("invalid remote image path")
	}
	return path, nil
}
