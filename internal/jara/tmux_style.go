package jara

import (
	"fmt"
	"strings"
)

// Style the session on every attachment so reconnecting also updates older
// sessions. Match the pane's saved terminal background and use contrasting text.
func configureTmuxStatus(session string) error {
	background, err := tmuxStatusBackground(session)
	if err != nil {
		return err
	}
	bg, fg := background.hex(), background.foreground().hex()
	accent := "#008080"
	if fg == "#d0d0d0" {
		accent = "#5fd7af"
	}
	base := "bg=" + bg + ",fg=" + fg
	options := [][2]string{
		{"status", "on"},
		{"status-position", "bottom"},
		{"status-style", base},
		{"status-justify", "left"},
		{"status-left-length", "36"},
		{"status-left", "#[fg=" + accent + ",bold] JARA #[default] #{=24:@jara_workspace}  "},
		{"status-right-length", "40"},
		{"status-right", "#{?#{>=:#{client_width},80},%H:%M  ,}Ctrl-b d detach "},
		{"window-status-format", " #I:#W "},
		{"window-status-style", base},
		{"window-status-current-format", " #I:#W "},
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
