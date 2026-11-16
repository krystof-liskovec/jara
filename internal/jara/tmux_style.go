package jara

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/textutil"
)

// Style the session on every attachment so reconnecting also updates older
// sessions. Match the pane's saved terminal background and use contrasting text.
// remoteName is what the attaching client calls this host; sessions keep the
// last one, so attaching over plain SSH shows the name used before.
func configureTmuxStatus(session, remoteName string) error {
	background, err := tmuxStatusBackground(session)
	if err != nil {
		return err
	}
	if remoteName = tmuxLabel(remoteName); remoteName != "" {
		if err := runTmux("set-option", "-t", session, "@jara_remote_name", remoteName); err != nil {
			return fmt.Errorf("set remote label: %w", err)
		}
	}
	workspaceName := tmuxWorkspaceName(session)
	if err := runTmux("set-option", "-t", session, "@jara_workspace_name", workspaceName); err != nil {
		return fmt.Errorf("set workspace label: %w", err)
	}
	if err := runTmux("set-option", "-s", "set-titles", "on"); err != nil {
		return fmt.Errorf("enable terminal titles: %w", err)
	}
	if err := runTmux("set-option", "-s", "set-titles-string", "Jara · #{@jara_workspace_name}"); err != nil {
		return fmt.Errorf("set terminal title: %w", err)
	}
	if err := runTmux("set-option", "-s", "status-interval", "15"); err != nil {
		return fmt.Errorf("set status refresh interval: %w", err)
	}
	remainOnExit := "off"
	if background, err := tmux("show-option", "-v", "-t", session, "@jara_background").Output(); err == nil && strings.TrimSpace(string(background)) == "1" {
		remainOnExit = "on"
	}
	if err := runTmux("set-window-option", "-t", session, "remain-on-exit", remainOnExit); err != nil {
		return fmt.Errorf("configure exited terminal panes: %w", err)
	}
	bg, fg := background.hex(), background.foreground().hex()
	accent := "#af3a03"
	if fg == "#d0d0d0" {
		accent = "#fe8019"
	}
	base := "bg=" + bg + ",fg=" + fg
	if err := runTmux("set-option", "-s", "escape-time", "40"); err != nil {
		return fmt.Errorf("set terminal Escape delay: %w", err)
	}
	options := [][2]string{
		{"mouse", "on"},
		{"history-limit", "10000"},
		{"status", "on"},
		{"status-position", "bottom"},
		{"status-style", base},
		{"status-justify", "left"},
		{"status-left-length", "56"},
		{"status-left", "#[fg=" + accent + ",bold] JARA #[default] #{?@jara_remote_name,#{=16:@jara_remote_name} · ,}#{=24:@jara_workspace_name}  "},
		{"status-right-length", "72"},
		{"status-right", "#{?#{>=:#{client_width},80},%H:%M  ,}#(/usr/local/bin/jara _session-count)"},
		{"window-status-format", ""},
		{"window-status-style", base},
		{"window-status-current-format", ""},
		{"window-status-current-style", "bg=" + bg + ",fg=" + accent + ",bold"},
		{"window-status-activity-style", base + ",underscore"},
		{"window-status-separator", " "},
	}
	for _, option := range options {
		if err := runTmux("set-option", "-t", session, option[0], option[1]); err != nil {
			return fmt.Errorf("style terminal status bar: %w", err)
		}
	}
	return nil
}

func tmuxWorkspaceName(session string) string {
	value, err := tmux("show-option", "-v", "-t", session, "@jara_workspace").Output()
	if err != nil {
		return "workspace"
	}
	id := strings.TrimSpace(string(value))
	if workspace, err := workspaceInfo(id); err == nil {
		name := workspaceTerminalTitle(workspace)
		if name != "Copied workspace" {
			return strings.ReplaceAll(name, "#", "＃")
		}
	}
	id = strings.TrimPrefix(id, "w-")
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

func tmuxSessionName(workspace Workspace, id string) string {
	label := strings.ToLower(workspaceTerminalTitle(workspace))
	var slug strings.Builder
	separator := false
	for _, r := range label {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			slug.WriteRune(r)
			separator = false
		} else if slug.Len() > 0 && !separator {
			slug.WriteByte('-')
			separator = true
		}
	}
	project := strings.Trim(slug.String(), "-")
	if len(project) > 18 {
		project = project[:18]
	}
	if project == "" {
		project = "workspace"
	}
	shortID := strings.TrimPrefix(id, "w-")
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return "jara-" + project + "-" + shortID + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func tmuxStatusBackground(session string) (terminalRGB, error) {
	// This is the color used for the running pane, including sessions created
	// before status bar styling was added.
	value, err := tmux("show-environment", "-t", session, "JARA_TMUX_BACKGROUND").Output()
	if err != nil {
		return terminalBackground()
	}
	color, configured, err := parseTerminalBackground(strings.TrimPrefix(strings.TrimSpace(string(value)), "JARA_TMUX_BACKGROUND="))
	if err != nil {
		return terminalRGB{}, err
	}
	if !configured {
		return terminalBackground()
	}
	return color, nil
}

// tmuxLabel makes text safe to show through a tmux format.
func tmuxLabel(text string) string {
	text = strings.Join(strings.Fields(textutil.SanitizeText(text)), " ")
	return strings.ReplaceAll(text, "#", "＃")
}
