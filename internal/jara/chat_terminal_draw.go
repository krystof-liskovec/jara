//go:build linux || darwin

package jara

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

// chatScreenState remembers the last frame so a redraw only rewrites the
// lines that changed.
type chatScreenState struct {
	lines         []string
	width, height int
	cursorShown   bool
}

func (t *chatTerminal) redraw(now time.Time) error {
	err := t.draw()
	t.lastDraw, t.dirty = now, false
	return err
}

func (t *chatTerminal) draw() error {
	width, height, err := term.GetSize(int(t.output.Fd()))
	if err != nil {
		width, height = 80, 24
	}
	frame := t.frame(max(1, width), max(1, height), time.Now())
	t.targets = frame
	_, err = io.WriteString(t.output, t.screen.update(frame, max(1, width), max(1, height)))
	return err
}

// frame renders the whole screen: the conversation and, on wide terminals,
// the changes pane.
func (t *chatTerminal) frame(width, height int, now time.Time) chatFrame {
	u, editor := t.u, t.editor
	color := os.Getenv("NO_COLOR") == ""
	composerText, composerCursor := editor.previewText()
	if u.login.secret != "" {
		composerText, composerCursor = chatMaskSecret(composerText, composerCursor)
	}
	cursorLine, cursorColumn := chatEditorCursorPosition(composerText, composerCursor)
	u.diff.screenWidth, u.diff.screenHeight = width, height
	if chatDiffFullScreen(&u.diff, width, height) {
		lines := renderChatDiffPane(&u.diff, width, height, now, t.motion, t.palette, color)
		return chatFrame{Lines: lines, CursorRow: -1, Targets: chatDiffPaneTargets(&u.diff, width, height, 0)}
	}
	paneWidth := chatDiffPaneWidth(&u.diff, width, height)
	width -= paneWidth
	transcriptWidth := width - 4
	if height < 7 || width < 18 {
		transcriptWidth = width
	}
	suggestions := t.suggestions()
	queueLines, queueEditNumber := chatTerminalQueue(u, t.queueEditor)
	activity, elapsed := chatTerminalActivity(u, now)
	transcript := t.agentTranscriptLines(transcriptWidth)
	if transcript == nil {
		transcript = t.transcript.transcriptLines(transcriptWidth)
	}
	agentLines, agentTargets := chatAgentStrip(u, now)
	composerTitle := ""
	vimMode := editor.vim
	if agent := u.viewedAgent(); agent != nil {
		composerTitle = chatAgentComposerLabel(agent)
	}
	if u.login.secret != "" {
		composerTitle = " " + chatSecretLabel(u.login.secret) + " · hidden "
		vimMode = chatVimOff
	}
	frame := renderChatView(chatViewModel{
		TranscriptLines: transcript, Agents: agentLines, AgentTargets: agentTargets, ComposerTitle: composerTitle,
		Composer: composerText, CursorByte: composerCursor, VimMode: vimMode, ProjectLocation: u.chatProjectLocation(),
		CursorLine: cursorLine, CursorColumn: cursorColumn, Suggestions: suggestions,
		ScrollOffset: t.scrollOffset, Activity: activity, ActivityElapsed: elapsed,
		ActivityFrame: int(now.UnixMilli() / 120), ActivitySeed: u.turnStartedAt.UnixNano(), ActivityHint: chatActivityHint(u),
		QueuedPrompts: queueLines, QueueEditing: t.queueEditor.active(), QueueEditNumber: queueEditNumber,
		ControlModel: u.modelControlLabel(), ControlReasoning: u.reasoningControlLabel(), ControlContext: u.contextControlLabel(),
		ControlHost:       chatControlHost(),
		ControlAutoOff:    u.manualApprovals,
		ApprovalPending:   len(u.approvals) > 0,
		ApprovalCountdown: u.approvalCountdown(now),
		Motion:            t.motion, Tick: int(now.UnixMilli() / chatShimmerStep.Milliseconds()),
		Mark: chatWelcomeMark(t.motion, now), Welcome: chatWelcome{Workspace: u.welcomeWorkspace(), Note: u.welcomeNote},
	}, width, height, t.palette, color)
	if paneWidth > 0 {
		pane := renderChatDiffPane(&u.diff, paneWidth-1, height, now, t.motion, t.palette, color)
		frame = composeChatScreen(frame, pane, t.palette, color)
		frame.Targets = append(frame.Targets, chatDiffPaneTargets(&u.diff, paneWidth-1, height, width+1)...)
	}
	return frame
}

// suggestions lists what the menu above the input box shows: an open picker,
// or completions for the text being typed.
func (t *chatTerminal) suggestions() []chatCompletionChoice {
	switch {
	case t.sessionPicker && t.u.sessions != nil:
		return chatSessionPickerChoices(t.u.sessions, t.sessionSelection)
	case t.picker.active():
		return t.picker.choices(t.u)
	case t.queueEditor.active():
		return nil
	}
	if choices, ok := t.questionChoices(); ok {
		return choices
	}
	return t.editor.Completions()
}

// update returns the escape sequences that turn the previous frame into
// this one.
func (s *chatScreenState) update(frame chatFrame, width, height int) string {
	var screen strings.Builder
	// Synchronized updates keep capable terminals from showing half a frame.
	screen.WriteString("\x1b[?2026h")
	fullRedraw := s.width != width || s.height != height || len(s.lines) != len(frame.Lines)
	if fullRedraw {
		screen.WriteString("\x1b[2J")
	}
	for index, line := range frame.Lines {
		if !fullRedraw && s.lines[index] == line {
			continue
		}
		fmt.Fprintf(&screen, "\x1b[%d;1H", index+1)
		screen.WriteString(line)
	}
	if frame.CursorRow >= 0 {
		fmt.Fprintf(&screen, "\x1b[%d;%dH", frame.CursorRow+1, frame.CursorCol+1)
		if !s.cursorShown {
			screen.WriteString("\x1b[?25h")
			s.cursorShown = true
		}
	} else {
		screen.WriteString("\x1b[?25l")
		s.cursorShown = false
	}
	screen.WriteString("\x1b[?2026l")
	s.lines = append(s.lines[:0], frame.Lines...)
	s.width, s.height = width, height
	return screen.String()
}
