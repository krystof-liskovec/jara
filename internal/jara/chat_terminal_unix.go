//go:build linux || darwin

package jara

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
	palette := menuColorsForTerminal()
	terminalOutput := &chatTerminalOutput{}
	u.out = terminalOutput
	if err := u.initializeChatServer(); err != nil {
		return err
	}
	workspace := u.workspaceLabel
	if workspace == "" {
		workspace = filepath.Base(filepath.Clean(u.cwd))
		if workspace == "." || workspace == string(filepath.Separator) {
			workspace = u.cwd
		}
	}
	editor := newChatEditor(maxBackgroundPrompt)
	parser := newChatTerminalKeyParser(maxBackgroundPrompt)
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
	var scrollOffset int
	resumePickerPending := u.resumeAtStart && !u.resumeLatest
	var sessionPicker bool
	var sessionSelection int
	picker := &chatTerminalPicker{}
	queueEditor := &chatQueueEditor{}
	lastActivityFrame := -1
	dirty := true
	lastDraw := time.Time{}
	var previousLines []string
	previousWidth, previousHeight := 0, 0
	cursorShown := false
	openPicker := func(kind string) {
		sessionPicker, resumePickerPending = false, false
		if err := picker.request(u, kind); err != nil {
			u.pickerRequested = ""
			terminalOutput.append("system", chatPickerErrorText("Could not open picker", err)+"\n")
		}
		dirty = true
	}
	draw := func() error {
		width, height, err := term.GetSize(int(output.Fd()))
		if err != nil {
			width, height = 80, 24
		}
		cursorLine, cursorColumn := editor.CursorPosition()
		width, height = max(1, width), max(1, height)
		transcriptWidth := width - 4
		if height < 7 || width < 18 {
			transcriptWidth = width
		}
		suggestions := editor.Completions()
		if queueEditor.active() {
			suggestions = nil
		}
		if sessionPicker && u.sessions != nil {
			suggestions = chatSessionPickerChoices(u.sessions, sessionSelection)
		} else if picker.active() {
			suggestions = picker.choices(u)
		}
		interruptHint := "Ctrl+C stop idle session"
		if u.agentTurnActive() || (u.busy && !u.persistentSession) {
			interruptHint = "Ctrl+C cancel turn"
		}
		tabHint := "Tab accept"
		if u.agentTurnActive() && u.threadReady && strings.TrimSpace(editor.Text()) != "" && len(suggestions) == 0 {
			tabHint = "Tab queue"
		}
		hints := []string{"↑↓ choose", tabHint, "PgUp/Dn/wheel scroll", "Shift+Enter newline", "Enter send", interruptHint}
		queueLines := u.queueViewLines(queueEditor.id)
		queueEditNumber := 0
		if queueEditor.active() {
			_, queueEditNumber, _, _ = u.queuedPromptByID(queueEditor.id)
			hints = []string{"Enter save edit", "Esc cancel", "Shift+Enter newline", "PgUp/Dn/wheel scroll"}
		} else if len(queueLines) > 0 {
			hints = append(hints, "/queue edit N")
		}
		if sessionPicker {
			hints = []string{"↑↓ choose", "Enter open", "Esc close", interruptHint}
		} else if picker.active() {
			hints = []string{"↑↓ choose", "Enter select", "Esc close", "F2 model", "F3 reasoning", interruptHint}
		}
		activity, elapsed := chatTerminalActivity(u, time.Now())
		frame := renderChatView(chatViewModel{
			Workspace: workspace, Model: u.effectiveModelLabel(), State: u.currentState(),
			TranscriptLines: terminalOutput.transcriptLines(transcriptWidth),
			Composer:        editor.Text(), CursorByte: editor.CursorByte(),
			CursorLine: cursorLine, CursorColumn: cursorColumn, Suggestions: suggestions,
			ScrollOffset: scrollOffset, Activity: activity, ActivityElapsed: elapsed,
			ActivityFrame: int(time.Now().UnixMilli() / 120),
			QueuedPrompts: queueLines, QueueEditing: queueEditor.active(), QueueEditNumber: queueEditNumber,
			ControlModel: u.modelControlLabel(), ControlReasoning: u.reasoningControlLabel(),
			Hints: hints,
		}, width, height, palette, os.Getenv("NO_COLOR") == "")
		var screen strings.Builder
		// Synchronized updates keep capable terminals from showing half a frame.
		screen.WriteString("\x1b[?2026h")
		fullRedraw := previousWidth != width || previousHeight != height || len(previousLines) != len(frame.Lines)
		if fullRedraw {
			screen.WriteString("\x1b[2J")
		}
		for index, line := range frame.Lines {
			if !fullRedraw && previousLines[index] == line {
				continue
			}
			fmt.Fprintf(&screen, "\x1b[%d;1H", index+1)
			screen.WriteString(line)
		}
		if frame.CursorRow >= 0 {
			fmt.Fprintf(&screen, "\x1b[%d;%dH", frame.CursorRow+1, frame.CursorCol+1)
			if !cursorShown {
				screen.WriteString("\x1b[?25h")
				cursorShown = true
			}
		} else {
			screen.WriteString("\x1b[?25l")
		}
		screen.WriteString("\x1b[?2026l")
		_, writeErr := io.WriteString(output, screen.String())
		previousLines = append(previousLines[:0], frame.Lines...)
		previousWidth, previousHeight = width, height
		return writeErr
	}
	if err := draw(); err != nil {
		return err
	}
	lastDraw, dirty = time.Now(), false

	for {
		select {
		case message := <-u.c.messages:
			if err := u.event(message); err != nil {
				return err
			}
			picker.sync(u)
			if queueEditor.active() {
				editor.SetCompletionContext(chatCompletionContext{})
			} else {
				editor.SetCompletionContext(chatCompletionContextFromUI(u))
			}
			if resumePickerPending && u.sessions != nil && u.sessions.complete {
				sessionPicker = u.sessions.failure == ""
				resumePickerPending = false
			}
			dirty = true
		case err := <-u.c.errors:
			terminalOutput.append("system", "Codex app-server disconnected: "+err.Error()+"\n")
			if drawErr := draw(); drawErr != nil {
				return drawErr
			}
			return fmt.Errorf("Codex app-server disconnected: %w", err)
		case data := <-inputChunks:
			for _, event := range parser.feed(data) {
				if event.scrollRows != 0 {
					scrollOffset = max(0, scrollOffset+event.scrollRows)
					dirty = true
					continue
				}
				if event.scroll != 0 {
					scrollOffset = max(0, scrollOffset+event.scroll*max(1, heightForChatTerminal(output)/2))
					dirty = true
					continue
				}
				if event.returnToLive {
					scrollOffset = 0
					dirty = true
					continue
				}
				if event.interrupt {
					if u.agentTurnActive() || (u.busy && !u.persistentSession) {
						if err := u.interrupt(); err != nil {
							return err
						}
					} else {
						if err := u.stopIdleSession(); err != nil {
							return err
						}
						return nil
					}
					dirty = true
					continue
				}
				if event.input == nil {
					continue
				}
				if event.input.Key == chatEditorModelPicker {
					openPicker("model")
					continue
				}
				if event.input.Key == chatEditorReasoningPicker {
					openPicker("reasoning")
					continue
				}
				if picker.active() {
					switch event.input.Key {
					case chatEditorUp:
						picker.move(u, -1)
						dirty = true
						continue
					case chatEditorDown:
						picker.move(u, 1)
						dirty = true
						continue
					case chatEditorEscape:
						picker.close()
						dirty = true
						continue
					case chatEditorEnter:
						if err := picker.choose(u); err != nil {
							terminalOutput.append("system", err.Error()+"\n")
						}
						dirty = true
						continue
					default:
						picker.close()
					}
				}
				if sessionPicker {
					switch event.input.Key {
					case chatEditorUp:
						sessionSelection = max(0, sessionSelection-1)
						dirty = true
						continue
					case chatEditorDown:
						if u.sessions != nil && len(u.sessions.matches) > 0 {
							sessionSelection = min(len(u.sessions.matches)-1, sessionSelection+1)
						}
						dirty = true
						continue
					case chatEditorEscape:
						sessionPicker = false
						resumePickerPending = false
						dirty = true
						continue
					case chatEditorEnter:
						if u.busy {
							terminalOutput.append("system", "A turn is running. Cancel or wait before opening a saved session.\n")
						} else if u.sessions != nil && u.sessions.complete && len(u.sessions.matches) > 0 {
							sessionPicker = false
							if err := u.resumeChatSessionChoice(strconv.Itoa(sessionSelection + 1)); err != nil {
								terminalOutput.append("system", err.Error()+"\n")
							}
						}
						dirty = true
						continue
					default:
						sessionPicker = false
						resumePickerPending = false
					}
				}
				if queueEditor.active() && event.input.Key == chatEditorEscape {
					queueEditor.cancel(u, editor)
					scrollOffset = 0
					dirty = true
					continue
				}
				if !queueEditor.active() && event.input.Key == chatEditorTab && u.agentTurnActive() && u.threadReady && strings.TrimSpace(editor.Text()) != "" && len(editor.Completions()) == 0 {
					if err := u.queuePrompt(u.thread, editor.Text()); err != nil {
						terminalOutput.append("system", err.Error()+"\n")
					} else {
						terminalOutput.append("system", fmt.Sprintf("Queued prompt %d. It will run after the current turn.\n", len(u.queuedPrompts)))
						editor.SetText("")
					}
					dirty = true
					continue
				}
				result := editor.Apply(*event.input)
				if result.LimitReached || event.limitReached {
					terminalOutput.append("system", "Input reached the size limit; the remaining text was omitted.\n")
				}
				if result.Submitted {
					if queueEditor.active() {
						queueEditor.save(u, editor, result.Value)
						scrollOffset = 0
						dirty = true
						continue
					}
					line := strings.TrimSpace(result.Value)
					if line != "" {
						isCommand := strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//")
						wasBusy, wasReady := u.busy, u.threadReady
						busyControl := wasBusy && (line == "/new" || strings.HasPrefix(line, "/resume ") || line == "/compose")
						if line == "/resume" {
							picker.close()
							resumePickerPending = true
							sessionPicker = true
							sessionSelection = 0
						} else if strings.HasPrefix(line, "/sessions") {
							resumePickerPending, sessionPicker = false, false
						}
						previousSessions := u.sessions
						quit, err := u.line(result.Value)
						if err != nil {
							return err
						}
						if quit {
							return nil
						}
						picker.sync(u)
						if line == "/resume" && u.sessions == previousSessions {
							resumePickerPending = false
							sessionPicker = false
						}
						if (!isCommand || busyControl) && (wasBusy || !wasReady) {
							editor.SetText(result.Value)
						}
					}
					if u.queueEditRequest != 0 {
						id := u.queueEditRequest
						u.queueEditRequest = 0
						if !queueEditor.begin(u, editor, id) {
							terminalOutput.append("system", "That queued prompt is no longer available to edit.\n")
						}
					}
					scrollOffset = 0
				}
				dirty = dirty || result.Changed || result.LimitReached || event.limitReached
			}
		case err := <-inputErrors:
			if err != nil && err != io.EOF {
				return fmt.Errorf("read interactive terminal: %w", err)
			}
			return nil
		case received := <-signals:
			switch received {
			case syscall.SIGWINCH:
				dirty = true
			case os.Interrupt:
				if u.agentTurnActive() || (u.busy && !u.persistentSession) {
					if err := u.interrupt(); err != nil {
						return err
					}
					dirty = true
				} else {
					if err := u.stopIdleSession(); err != nil {
						return err
					}
					return nil
				}
			default:
				return nil
			}
		case now := <-ticker.C:
			if event := parser.flushEscape(now); event != nil && event.input != nil {
				if queueEditor.active() {
					queueEditor.cancel(u, editor)
					scrollOffset = 0
					dirty = true
				} else {
					result := editor.Apply(*event.input)
					dirty = dirty || result.Changed
				}
			}
			for id, pending := range u.pending {
				if now.After(pending.deadline) {
					if pending.method == "thread/list" {
						u.expireChatSessionRequest(id)
						if resumePickerPending && u.sessions != nil && u.sessions.complete {
							sessionPicker = u.sessions.failure == ""
							resumePickerPending = false
						}
						dirty = true
						continue
					}
					if pending.method == "model/list" {
						u.expireChatModelRequest(id)
						dirty = true
						continue
					}
					return fmt.Errorf("Codex app-server timed out during %s", pending.method)
				}
			}
			picker.sync(u)
			if u.busy && u.turnStartedAt.IsZero() && u.activityLabel != "" {
				// Preserve a useful timer while the start request is pending.
				u.turnStartedAt = now
			}
			activityFrame := int(now.UnixMilli() / 120)
			if u.busy && activityFrame != lastActivityFrame {
				lastActivityFrame = activityFrame
				dirty = true
			}
			if dirty && (lastDraw.IsZero() || now.Sub(lastDraw) >= chatTerminalRefresh) {
				if err := draw(); err != nil {
					return err
				}
				lastDraw, dirty = now, false
			}
		}
		if dirty && time.Since(lastDraw) >= chatTerminalRefresh {
			if err := draw(); err != nil {
				return err
			}
			lastDraw, dirty = time.Now(), false
		}
	}
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

func heightForChatTerminal(output *os.File) int {
	_, height, err := term.GetSize(int(output.Fd()))
	if err != nil || height < 1 {
		return 24
	}
	return height
}
