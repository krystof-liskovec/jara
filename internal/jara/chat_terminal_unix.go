//go:build linux || darwin

package jara

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	chatTerminalRefresh = 80 * time.Millisecond
	chatTranscriptLimit = 512 * 1024
)

type chatTerminalOutput struct {
	messages []chatViewMessage
	bytes    int
	trimmed  bool
}

func (o *chatTerminalOutput) Write(data []byte) (int, error) {
	text := string(data)
	text = strings.ReplaceAll(text, "You> ", "")
	text = strings.ReplaceAll(text, "Compose> ", "")
	if strings.HasPrefix(text, "Jara conversation · /help for commands · Ctrl-C cancels a turn\n") {
		text = strings.TrimPrefix(text, "Jara conversation · /help for commands · Ctrl-C cancels a turn\n")
	}
	o.append("system", text)
	return len(data), nil
}

func (o *chatTerminalOutput) WriteChatRole(role, text string) { o.append(role, text) }

func (o *chatTerminalOutput) append(role, text string) {
	if text == "" {
		return
	}
	text = chatText(text)
	if count := len(text); count > chatTranscriptLimit {
		text, _ = truncateChatGraphemes(text, chatTranscriptLimit)
		o.trimmed = true
	}
	if count := len(o.messages); count > 0 && o.messages[count-1].Role == role {
		o.messages[count-1].Text += text
	} else {
		o.messages = append(o.messages, chatViewMessage{Role: role, Text: text})
	}
	o.bytes += len(text)
	for o.bytes > chatTranscriptLimit && len(o.messages) > 1 {
		o.bytes -= len(o.messages[0].Text)
		o.messages = o.messages[1:]
		o.trimmed = true
	}
}

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
		_, _ = output.Write([]byte("\x1b[?2004l\x1b[?25h\x1b[?1049l"))
		if restoreErr := term.Restore(int(input.Fd()), state); returnErr == nil && restoreErr != nil {
			returnErr = fmt.Errorf("restore terminal: %w", restoreErr)
		}
	}()
	if _, err := output.Write([]byte("\x1b[?1049h\x1b[?25l\x1b[?2004h\x1b[2J\x1b[H")); err != nil {
		return err
	}
	palette := menuColorsForTerminal()
	terminalOutput := &chatTerminalOutput{}
	u.out = terminalOutput
	if err := u.request("initialize", map[string]any{"clientInfo": map[string]string{"name": "jara", "title": "Jara", "version": Version}}); err != nil {
		return err
	}
	workspace := filepath.Base(filepath.Clean(u.cwd))
	if workspace == "." || workspace == string(filepath.Separator) {
		workspace = u.cwd
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
	dirty := true
	lastDraw := time.Time{}
	draw := func() error {
		width, height, err := term.GetSize(int(output.Fd()))
		if err != nil {
			width, height = 80, 24
		}
		choices := editor.Completions()
		cursorLine, cursorColumn := editor.CursorPosition()
		messages := terminalOutput.messages
		if terminalOutput.trimmed {
			messages = append([]chatViewMessage{{Role: "system", Text: "Earlier output was trimmed to keep this session responsive."}}, messages...)
		}
		frame := renderChatView(chatViewModel{
			Workspace: workspace, Model: u.effectiveModelLabel(), State: u.currentState(),
			Messages: messages, Composer: editor.Text(), CursorByte: editor.CursorByte(),
			CursorLine: cursorLine, CursorColumn: cursorColumn, Suggestions: choices,
			ScrollOffset: scrollOffset,
			Hints:        []string{"↑↓ choose", "Tab/Enter complete", "PgUp/PgDn scroll", "Ctrl+J newline", "Enter send", "Ctrl+C cancel/quit"},
		}, width, height, palette, os.Getenv("NO_COLOR") == "")
		var screen strings.Builder
		screen.WriteString("\x1b[H")
		for index, line := range frame.Lines {
			if index > 0 {
				screen.WriteString("\r\n")
			}
			screen.WriteString(line)
		}
		if frame.CursorRow >= 0 {
			fmt.Fprintf(&screen, "\x1b[%d;%dH\x1b[?25h", frame.CursorRow+1, frame.CursorCol+1)
		} else {
			screen.WriteString("\x1b[?25l")
		}
		_, writeErr := io.WriteString(output, screen.String())
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
			editor.SetCompletionContext(chatCompletionContextFromUI(u))
			dirty = true
		case err := <-u.c.errors:
			terminalOutput.append("system", "Codex app-server disconnected: "+err.Error()+"\n")
			if drawErr := draw(); drawErr != nil {
				return drawErr
			}
			return fmt.Errorf("Codex app-server disconnected: %w", err)
		case data := <-inputChunks:
			for _, event := range parser.feed(data) {
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
					if u.busy {
						if err := u.interrupt(); err != nil {
							return err
						}
					} else {
						return nil
					}
					dirty = true
					continue
				}
				if event.input == nil {
					continue
				}
				result := editor.Apply(*event.input)
				if result.LimitReached || event.limitReached {
					terminalOutput.append("system", "Input reached the size limit; the remaining text was omitted.\n")
				}
				if result.Submitted {
					line := strings.TrimSpace(result.Value)
					if line != "" {
						isCommand := strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//")
						busyControl := u.busy && (line == "/new" || strings.HasPrefix(line, "/resume") || line == "/compose")
						quit, err := u.line(result.Value)
						if err != nil {
							return err
						}
						if quit {
							return nil
						}
						if (!isCommand || busyControl) && (u.busy || !u.threadReady) {
							editor.SetText(result.Value)
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
				if u.busy {
					if err := u.interrupt(); err != nil {
						return err
					}
					dirty = true
				} else {
					return nil
				}
			default:
				return nil
			}
		case now := <-ticker.C:
			if event := parser.flushEscape(now); event != nil && event.input != nil {
				result := editor.Apply(*event.input)
				dirty = dirty || result.Changed
			}
			for id, pending := range u.pending {
				if now.After(pending.deadline) {
					if pending.method == "thread/list" {
						u.expireChatSessionRequest(id)
						continue
					}
					if pending.method == "model/list" {
						u.expireChatModelRequest(id)
						continue
					}
					return fmt.Errorf("Codex app-server timed out during %s", pending.method)
				}
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
