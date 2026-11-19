//go:build linux || darwin

package jara

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	chatTerminalRefresh = 16 * time.Millisecond
)

func chatInteractiveFiles(input io.Reader, output io.Writer) (*os.File, *os.File, bool) {
	if os.Getenv("JARA_CHAT_PLAIN") == "1" || os.Getenv("TERM") == "dumb" {
		return nil, nil, false
	}
	in, inputOK := input.(*os.File)
	out, outputOK := output.(*os.File)
	if !inputOK || !outputOK || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return nil, nil, false
	}
	return in, out, true
}

// chatTerminal is the full-screen interface: it owns the input box, the
// pickers, and the screen, and feeds the user's actions to the chatUI.
type chatTerminal struct {
	u          *chatUI
	output     *os.File
	transcript *chatTerminalOutput
	palette    menuPalette
	motion     bool

	editor      *chatEditor
	parser      *chatTerminalKeyParser
	picker      *chatTerminalPicker
	queueEditor *chatQueueEditor

	scrollOffset        int
	resumePickerPending bool // open the session picker once sessions load
	sessionPicker       bool
	sessionSelection    int

	agents                chatTerminalAgents
	dirty                 bool
	targets               chatFrame // the last frame drawn, for taps
	lastDraw              time.Time
	lastActivityFrame     int
	lastApprovalCountdown string
	question              chatQuestionMenu
	screen                chatScreenState
}

func newChatTerminal(u *chatUI, output *os.File) *chatTerminal {
	t := &chatTerminal{
		u: u, output: output, transcript: &chatTerminalOutput{},
		palette: menuColorsForTerminal(), motion: chatMotionEnabled(),
		editor: newChatEditor(maxBackgroundPrompt), parser: newChatTerminalKeyParser(maxBackgroundPrompt),
		picker: &chatTerminalPicker{}, queueEditor: &chatQueueEditor{},
		resumePickerPending: u.resumeAtStart && !u.resumeLatest,
		lastActivityFrame:   -1, dirty: true,
	}
	u.out = t.transcript
	u.vimCommand = t.editor.vimCommand
	u.alert = func(sequence string) { _, _ = io.WriteString(output, sequence) }
	return t
}

func chatTerminalLoop(u *chatUI, input, output *os.File) (returnErr error) {
	state, err := term.MakeRaw(int(input.Fd()))
	if err != nil {
		return fmt.Errorf("enable interactive terminal: %w", err)
	}
	defer func() {
		_, _ = output.Write([]byte("\x1b[?1006l\x1b[?1000l\x1b[>4;0m\x1b[<u\x1b[?2026l\x1b[?2004l\x1b[0 q\x1b[?25h\x1b[?1049l"))
		if restoreErr := term.Restore(int(input.Fd()), state); returnErr == nil && restoreErr != nil {
			returnErr = fmt.Errorf("restore terminal: %w", restoreErr)
		}
	}()
	if _, err := output.Write([]byte("\x1b[?1049h\x1b[?25l\x1b[2 q\x1b[?2004h\x1b[?1000h\x1b[?1006h\x1b[>1u\x1b[>4;2m")); err != nil {
		return err
	}
	t := newChatTerminal(u, output)
	if err := u.initializeChatServer(); err != nil {
		return err
	}
	inputChunks := make(chan []byte, 16)
	inputErrors := make(chan error, 1)
	readerDone := make(chan struct{})
	defer close(readerDone)
	go readChatTerminal(input, inputChunks, inputErrors, readerDone)

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(signals)
	ticker := time.NewTicker(chatTerminalRefresh)
	defer ticker.Stop()
	if err := t.redraw(time.Now()); err != nil {
		return err
	}
	for {
		var done bool
		var err error
		select {
		case message := <-u.c.messages:
			err = t.handleMessage(message)
		case connectionErr := <-u.c.errors:
			if u.connectionLost(connectionErr) {
				err = t.draw()
				break
			}
			t.transcript.append("system", "Codex app-server disconnected: "+connectionErr.Error()+"\n")
			if drawErr := t.draw(); drawErr != nil {
				return drawErr
			}
			return fmt.Errorf("Codex app-server disconnected: %w", connectionErr)
		case result := <-u.reconnectRuns:
			if err = u.acceptReconnect(result); err == nil {
				err = t.draw()
			}
		case text := <-u.backgroundRuns:
			u.acceptChatBackground(text)
			err = t.draw()
		case data := <-inputChunks:
			for _, event := range t.parser.feed(data) {
				if done, err = t.handleKey(event); done || err != nil {
					break
				}
			}
		case readErr := <-inputErrors:
			if readErr != nil && readErr != io.EOF {
				return fmt.Errorf("read interactive terminal: %w", readErr)
			}
			return nil
		case received := <-signals:
			done, err = t.handleSignal(received)
		case now := <-ticker.C:
			err = t.tick(now)
		}
		if done || err != nil {
			return err
		}
		if t.dirty && time.Since(t.lastDraw) >= chatTerminalRefresh {
			if err := t.redraw(time.Now()); err != nil {
				return err
			}
		}
	}
}

func (t *chatTerminal) handleMessage(message appMessage) error {
	u := t.u
	if err := u.event(message); err != nil {
		return err
	}
	t.picker.sync(u)
	if !t.queueEditor.active() {
		t.applyPrefill()
	}
	if t.queueEditor.active() {
		t.editor.SetCompletionContext(chatCompletionContext{})
	} else {
		t.editor.SetCompletionContext(chatCompletionContextFromUI(u))
	}
	t.openLoadedSessionPicker()
	t.dirty = true
	return nil
}

// applyPrefill puts text the chatUI handed back, such as a rewound prompt,
// into the input box.
func (t *chatTerminal) applyPrefill() {
	if t.u.prefill != "" {
		t.editor.SetText(t.u.prefill)
		t.u.prefill = ""
	}
}

func (t *chatTerminal) openLoadedSessionPicker() {
	if t.resumePickerPending && t.u.sessions != nil && t.u.sessions.complete {
		t.sessionPicker = t.u.sessions.failure == ""
		t.resumePickerPending = false
	}
}

func (t *chatTerminal) handleSignal(received os.Signal) (done bool, err error) {
	switch received {
	case syscall.SIGWINCH:
		t.dirty = true
		return false, nil
	case os.Interrupt:
		return t.interrupt()
	default:
		return true, nil
	}
}

// interrupt cancels a running turn, or stops an idle session.
func (t *chatTerminal) interrupt() (done bool, err error) {
	u := t.u
	if u.agentTurnActive() || (u.busy && !u.persistentSession) {
		t.dirty = true
		return false, u.interrupt()
	}
	return true, u.stopIdleSession()
}

func (t *chatTerminal) tick(now time.Time) error {
	u := t.u
	if event := t.parser.flushEscape(now); event != nil && event.input != nil {
		if _, err := t.handleKey(*event); err != nil { // a lone Escape key
			return err
		}
	}
	for id, pending := range u.pending {
		if !now.After(pending.deadline) {
			continue
		}
		if pending.method == "thread/list" {
			u.expireChatSessionRequest(id)
			t.openLoadedSessionPicker()
		} else if err := u.expireChatRequest(id, pending); err != nil {
			return err
		}
		t.dirty = true
	}
	if answered, err := u.autoApproveExpired(now); err != nil {
		return err
	} else if answered {
		t.dirty = true
	}
	if countdown := u.approvalCountdown(now); countdown != t.lastApprovalCountdown {
		t.lastApprovalCountdown = countdown
		t.dirty = true
	}
	t.picker.sync(u)
	if err := t.tickAgents(now); err != nil {
		return err
	}
	u.refreshGitDiffWhileShown(now)
	if u.refreshChatProjectLocation(now) {
		t.dirty = true
	}
	if u.busy && u.turnStartedAt.IsZero() && u.activityLabel != "" {
		// Preserve a useful timer while the start request is pending.
		u.turnStartedAt = now
	}
	if frame := chatAnimationFrame(u, t.motion, now); frame >= 0 && frame != t.lastActivityFrame {
		t.lastActivityFrame = frame
		t.dirty = true
	}
	if t.dirty && (t.lastDraw.IsZero() || now.Sub(t.lastDraw) >= chatTerminalRefresh) {
		return t.redraw(now)
	}
	return nil
}

func readChatTerminal(input *os.File, chunks chan<- []byte, errors chan<- error, done <-chan struct{}) {
	buffer := make([]byte, 4096)
	for {
		n, err := input.Read(buffer)
		if n > 0 {
			data := append([]byte(nil), buffer[:n]...)
			select {
			case chunks <- data:
			case <-done:
				return
			}
		}
		if err != nil {
			select {
			case errors <- err:
			case <-done:
			}
			return
		}
	}
}

// menuWidthForChatTerminal is the width menus above the input box get: the
// terminal's, less the changes pane when it shows.
func (t *chatTerminal) menuWidth() int {
	width, height, err := term.GetSize(int(t.output.Fd()))
	if err != nil || width < 1 {
		width, height = 80, 24
	}
	return width - chatDiffPaneWidth(&t.u.diff, width, height)
}

func heightForChatTerminal(output *os.File) int {
	_, height, err := term.GetSize(int(output.Fd()))
	if err != nil || height < 1 {
		return 24
	}
	return height
}
