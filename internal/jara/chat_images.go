package jara

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Images join a prompt as [Image #N] placeholders: paste or drop an image
// file's path, press Ctrl+V with an image on the clipboard, or use /image
// PATH. The image is read where this interface runs and sent with the
// prompt, so it works with remote workspaces too.

const chatImageLimit = 5 << 20 // the smallest per-image limit of the providers

var chatImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

func (u *chatUI) attachChatImage(data []byte) (string, error) {
	if len(data) > chatImageLimit {
		return "", fmt.Errorf("the image is larger than %d MB", chatImageLimit>>20)
	}
	mime := http.DetectContentType(data)
	if !chatImageTypes[mime] {
		return "", fmt.Errorf("only PNG, JPEG, GIF, and WebP images can be attached")
	}
	if u.images == nil {
		u.images = map[int]string{}
	}
	number := 1
	for existing := range u.images {
		number = max(number, existing+1)
	}
	u.images[number] = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("[Image #%d]", number), nil
}

func (u *chatUI) attachChatImageFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() || info.Size() > chatImageLimit {
		return "", fmt.Errorf("%s is not an image of at most %d MB", filepath.Base(path), chatImageLimit>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return u.attachChatImage(data)
}

// pastedChatImage turns a pasted or dropped image path into a placeholder;
// any other paste is returned unchanged.
func (u *chatUI) pastedChatImage(text string) (string, bool) {
	path := chatPastedPath(text)
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
	default:
		return text, false
	}
	placeholder, err := u.attachChatImageFile(path)
	if err != nil {
		return text, false
	}
	return placeholder + " ", true
}

func chatPastedPath(text string) string {
	path := strings.TrimSpace(text)
	if strings.ContainsAny(path, "\r\n") {
		return ""
	}
	if len(path) >= 2 && (path[0] == '\'' || path[0] == '"') && path[len(path)-1] == path[0] {
		path = path[1 : len(path)-1]
	}
	if parsed, err := url.Parse(path); err == nil && parsed.Scheme == "file" {
		path = parsed.Path
	}
	path = strings.ReplaceAll(path, `\ `, " ")
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

// clipboardChatImage reads an image from the system clipboard of the machine
// this interface runs on.
func clipboardChatImage() ([]byte, error) {
	var commands [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		commands = append(commands, []string{"wl-paste", "--no-newline", "--type", "image/png"})
	}
	if os.Getenv("DISPLAY") != "" {
		commands = append(commands, []string{"xclip", "-selection", "clipboard", "-target", "image/png", "-out"})
	}
	commands = append(commands, []string{"pngpaste", "-"})
	for _, command := range commands {
		if _, err := exec.LookPath(command[0]); err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		data, err := exec.CommandContext(ctx, command[0], command[1:]...).Output()
		cancel()
		if err == nil && len(data) > 0 {
			return data, nil
		}
	}
	return nil, fmt.Errorf("no image on the clipboard here; paste or drop an image file's path, or use /image PATH")
}

// clipboardChatImagePlaceholder attaches the clipboard image, reporting
// problems in the transcript.
func (u *chatUI) clipboardChatImagePlaceholder() string {
	data, err := clipboardChatImage()
	if err == nil {
		var placeholder string
		if placeholder, err = u.attachChatImage(data); err == nil {
			return placeholder + " "
		}
	}
	u.printRole("system", "Could not attach an image: "+err.Error()+".\n")
	return ""
}

func (u *chatUI) runChatImageCommand(args string) {
	if args == "" {
		u.print("Use /image PATH to attach an image, or paste an image file's path, or press Ctrl+V with an image on the clipboard.\n")
		return
	}
	placeholder, err := u.attachChatImageFile(chatPastedPath(args))
	if err != nil {
		u.printRole("system", "Could not attach the image: "+err.Error()+"\n")
		return
	}
	u.prefill = placeholder + " "
	u.printRole("system", "Attached "+placeholder+"; it is sent with the message that mentions it.\n")
}

// takeChatImages returns the images a message mentions as turn inputs and
// forgets them.
func (u *chatUI) takeChatImages(message string) []any {
	var numbers []int
	for number := range u.images {
		if strings.Contains(message, fmt.Sprintf("[Image #%d]", number)) {
			numbers = append(numbers, number)
		}
	}
	sort.Ints(numbers)
	inputs := make([]any, 0, len(numbers))
	for _, number := range numbers {
		inputs = append(inputs, map[string]any{"type": "image", "url": u.images[number]})
		delete(u.images, number)
	}
	return inputs
}
