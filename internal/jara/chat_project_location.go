package jara

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rivo/uniseg"
	"krystof.liskovec/jara/internal/textutil"
)

const (
	chatProjectLocationMethod  = "jara/project/location"
	chatProjectLocationRefresh = 5 * time.Second
)

type chatProjectLocation struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
}

type chatProjectStatus struct {
	location                chatProjectLocation
	lastCheck               time.Time
	requesting, unsupported bool
	localResult             <-chan chatProjectLocation
}

func abbreviateChatProjectPath(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil || cwd == "" {
		return cwd
	}
	rel, err := filepath.Rel(home, cwd)
	switch {
	case err != nil, rel == "..", strings.HasPrefix(rel, ".."+string(filepath.Separator)):
		return cwd
	case rel == ".":
		return "~"
	default:
		return "~" + string(filepath.Separator) + rel
	}
}

func collectChatProjectLocation(cwd string) chatProjectLocation {
	location := chatProjectLocation{Path: abbreviateChatProjectPath(cwd)}
	if cwd == "" {
		return location
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if head, err := chatGit(ctx, cwd, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		if branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "refs/heads/"); ok {
			location.Branch = branch
		}
	}
	return location
}

func handleChatProjectLocationRequest(id json.RawMessage, cwd string, client *chatDaemonClient) {
	go func() {
		result := collectChatProjectLocation(cwd)
		if line, err := json.Marshal(map[string]any{"id": id, "result": result}); err == nil {
			_ = client.write(line)
		}
	}()
}

func (u *chatUI) chatProjectLocation() chatProjectLocation {
	if u.project.location.Path != "" {
		return u.project.location
	}
	if u.persistentSession {
		return chatProjectLocation{Path: u.cwd}
	}
	return chatProjectLocation{Path: abbreviateChatProjectPath(u.cwd)}
}

// The branch can change during a session. Check the workspace in the
// background so the composer stays responsive to input.
func (u *chatUI) refreshChatProjectLocation(now time.Time) bool {
	p := &u.project
	if p.localResult != nil {
		select {
		case location := <-p.localResult:
			changed := location != p.location
			p.location, p.localResult = location, nil
			return changed
		default:
			return false
		}
	}
	if u.cwd == "" || p.requesting || p.unsupported || now.Sub(p.lastCheck) < chatProjectLocationRefresh {
		return false
	}
	if u.persistentSession {
		if u.chatOffline() || !u.connected || u.c == nil || u.c.encoder == nil {
			return false
		}
		p.requesting = u.request(chatProjectLocationMethod, nil) == nil
	} else {
		result := make(chan chatProjectLocation, 1)
		p.localResult = result
		go func(cwd string) { result <- collectChatProjectLocation(cwd) }(u.cwd)
	}
	p.lastCheck = now
	return false
}

func (u *chatUI) acceptChatProjectLocation(m appMessage) {
	u.project.requesting = false
	if m.Error != nil {
		u.project.unsupported = true
		return
	}
	var location chatProjectLocation
	if json.Unmarshal(m.Result, &location) == nil && location.Path != "" {
		u.project.location = location
	}
}

func (p chatProjectLocation) label(width int) string {
	path, branch := textutil.SanitizeText(p.Path), textutil.SanitizeText(p.Branch)
	if path == "" {
		return ""
	}
	if branch == "" {
		return fitChatProjectPath(path, width)
	}
	const separator = " on  "
	if width < len(separator)+4 {
		return textutil.FitText(path, width)
	}
	branchWidth := uniseg.StringWidth(branch)
	pathWidth := max((width-len(separator))/2, width-len(separator)-branchWidth)
	path = fitChatProjectPath(path, pathWidth)
	return path + separator + textutil.FitText(branch, width-len(separator)-uniseg.StringWidth(path))
}

func fitChatProjectPath(path string, width int) string {
	if uniseg.StringWidth(path) > width {
		if slash := strings.LastIndexByte(path, '/'); slash >= 0 && slash < len(path)-1 && width > 3 {
			path = "…" + path[slash:]
		}
	}
	return textutil.FitText(path, width)
}
