package jara

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"time"
)

const maxClipboardImage = 20 << 20

type boundedImageBuffer struct{ data bytes.Buffer }

func (b *boundedImageBuffer) Len() int      { return b.data.Len() }
func (b *boundedImageBuffer) Bytes() []byte { return b.data.Bytes() }

func (b *boundedImageBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxClipboardImage {
		return 0, fmt.Errorf("clipboard image exceeds 20 MiB")
	}
	return b.data.Write(p)
}

func readClipboardImage() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if runtime.GOOS == "darwin" {
		f, err := os.CreateTemp("", "jara-clipboard-*.png")
		if err != nil {
			return nil, err
		}
		name := f.Name()
		f.Close()
		defer os.Remove(name)
		script := `on run argv
set destination to POSIX file (item 1 of argv)
set imageData to the clipboard as «class PNGf»
set outputFile to open for access destination with write permission
try
set eof outputFile to 0
write imageData to outputFile
close access outputFile
on error messageText
close access outputFile
error messageText
end try
end run`
		if err = exec.CommandContext(ctx, "osascript", "-e", script, name).Run(); err != nil {
			return nil, fmt.Errorf("could not read an image from the macOS clipboard")
		}
		f, err = os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(io.LimitReader(f, maxClipboardImage+1))
	}
	var commands [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = append(commands, []string{"wl-paste", "--type", "image/png"}, []string{"wl-paste", "--type", "image/jpeg"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, []string{"xclip", "-selection", "clipboard", "-t", "image/jpeg", "-o"})
	}
	for _, args := range commands {
		var data boundedImageBuffer
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Stdout = &data
		if cmd.Run() == nil && data.Len() > 0 {
			return data.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("cannot read a clipboard image; copy a PNG/JPEG and install wl-clipboard (Wayland) or xclip (X11) locally")
}

func uploadClipboardImage(r Remote) (string, error) {
	data, err := readClipboardImage()
	if err != nil {
		return "", err
	}
	if _, err = clipboardImageFormat(data); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	args := append(sshArgs(r, false), "-o", "BatchMode=yes", "--", r.Host, shellArgs("jara", "_rpc", token(Request{Op: "image-upload"})))
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("image upload failed: %w; %s (the upload SSH connection needs an available key/ssh-agent)", err, stderr.String())
	}
	return decodeClipboardPath(out.Bytes())
}
