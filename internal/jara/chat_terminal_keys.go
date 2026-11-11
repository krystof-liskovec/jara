package jara

import (
	"bytes"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	chatTerminalPasteStart = "\x1b[200~"
	chatTerminalPasteEnd   = "\x1b[201~"
	chatTerminalEscDelay   = 35 * time.Millisecond
)

type chatTerminalKeyEvent struct {
	input        *chatEditorInput
	diffPane     bool // toggle the changes pane
	paneScroll   int  // rows to scroll the changes pane; negative scrolls up
	scroll       int
	scrollRows   int
	returnToLive bool
	interrupt    bool
	limitReached bool
}

type chatTerminalKeyParser struct {
	buffer      []byte
	paste       bool
	pasteBytes  []byte
	pasteTail   []byte
	lastEscape  time.Time
	maxPaste    int
	pasteCapped bool
}

func newChatTerminalKeyParser(maxPaste int) *chatTerminalKeyParser {
	return &chatTerminalKeyParser{maxPaste: maxPaste}
}

func (p *chatTerminalKeyParser) feed(data []byte) []chatTerminalKeyEvent {
	p.buffer = append(p.buffer, data...)
	var events []chatTerminalKeyEvent
	for len(p.buffer) > 0 {
		if p.paste {
			pasteEvents := p.consumePaste()
			if pasteEvents != nil {
				events = append(events, pasteEvents...)
			}
			if p.paste {
				break
			}
			continue
		}
		if p.buffer[0] == 0x1b {
			consumed, event, waiting := p.consumeEscape()
			if waiting {
				break
			}
			p.buffer = p.buffer[consumed:]
			if event != nil {
				events = append(events, *event)
			}
			continue
		}
		b := p.buffer[0]
		switch b {
		case '\r':
			events = append(events, editorEvent(chatEditorEnter))
			p.buffer = p.buffer[1:]
		case '\n':
			// Ctrl+J inserts a newline into the multiline composer.
			events = append(events, editorEvent(chatEditorText, "\n"))
			p.buffer = p.buffer[1:]
		case '\t':
			events = append(events, editorEvent(chatEditorTab))
			p.buffer = p.buffer[1:]
		case 0x03:
			events = append(events, chatTerminalKeyEvent{interrupt: true})
			p.buffer = p.buffer[1:]
		case 0x16: // Ctrl+V attaches an image from the clipboard
			events = append(events, editorEvent(chatEditorClipboardImage))
			p.buffer = p.buffer[1:]
		case 0x08, 0x7f:
			events = append(events, editorEvent(chatEditorBackspace))
			p.buffer = p.buffer[1:]
		default:
			if b < 0x20 {
				p.buffer = p.buffer[1:]
				continue
			}
			if !utf8.FullRune(p.buffer) {
				return events
			}
			r, size := utf8.DecodeRune(p.buffer)
			if r == utf8.RuneError && size == 1 {
				events = append(events, editorEvent(chatEditorText, string(utf8.RuneError)))
			} else {
				events = append(events, editorEvent(chatEditorText, string(r)))
			}
			p.buffer = p.buffer[size:]
		}
	}
	return events
}

func (p *chatTerminalKeyParser) flushEscape(now time.Time) *chatTerminalKeyEvent {
	if len(p.buffer) != 1 || p.buffer[0] != 0x1b || p.lastEscape.IsZero() || now.Sub(p.lastEscape) < chatTerminalEscDelay {
		return nil
	}
	p.buffer = p.buffer[1:]
	p.lastEscape = time.Time{}
	event := editorEvent(chatEditorEscape)
	return &event
}

func (p *chatTerminalKeyParser) consumeEscape() (int, *chatTerminalKeyEvent, bool) {
	if len(p.buffer) == 1 {
		if p.lastEscape.IsZero() {
			p.lastEscape = time.Now()
		}
		return 0, nil, true
	}
	if p.buffer[1] == '\r' || p.buffer[1] == '\n' {
		p.lastEscape = time.Time{}
		event := editorEvent(chatEditorText, "\n")
		return 2, &event, false
	}
	p.lastEscape = time.Time{}
	if p.buffer[1] != '[' && p.buffer[1] != 'O' {
		event := editorEvent(chatEditorEscape)
		return 1, &event, false
	}
	for i := 2; i < len(p.buffer); i++ {
		if p.buffer[i] < 0x40 || p.buffer[i] > 0x7e {
			continue
		}
		sequence := string(p.buffer[:i+1])
		event := p.escapeEvent(sequence)
		if sequence == chatTerminalPasteStart {
			p.paste = true
			p.pasteBytes = p.pasteBytes[:0]
			p.pasteTail = p.pasteTail[:0]
			p.pasteCapped = false
			return i + 1, nil, false
		}
		if event == nil {
			e := editorEvent(chatEditorEscape)
			return i + 1, &e, false
		}
		return i + 1, event, false
	}
	if len(p.buffer) > 64 {
		event := editorEvent(chatEditorEscape)
		return len(p.buffer), &event, false
	}
	return 0, nil, true
}

func (p *chatTerminalKeyParser) escapeEvent(sequence string) *chatTerminalKeyEvent {
	if event := chatTerminalMouseEvent(sequence); event != nil {
		return event
	}
	switch chatFunctionKey(sequence) {
	case 2:
		event := editorEvent(chatEditorModelPicker)
		return &event
	case 3:
		event := editorEvent(chatEditorReasoningPicker)
		return &event
	case 4:
		return &chatTerminalKeyEvent{diffPane: true}
	case 5:
		return &chatTerminalKeyEvent{paneScroll: -chatDiffPaneScrollStep}
	case 6:
		return &chatTerminalKeyEvent{paneScroll: chatDiffPaneScrollStep}
	}
	switch sequence {
	case "\x1b[1;5C", "\x1b[1;5:1C", "\x1b[5C", "\x1b[57351;5u", "\x1b[57351;5:1u":
		event := editorEvent(chatEditorWordRight)
		return &event
	case "\x1b[1;5D", "\x1b[1;5:1D", "\x1b[5D", "\x1b[57350;5u", "\x1b[57350;5:1u":
		event := editorEvent(chatEditorWordLeft)
		return &event
	case "\x1b[13;2u", "\x1b[13;2:1u", "\x1b[27;2;13~",
		"\x1b[13;3u", "\x1b[13;3:1u", "\x1b[27;3;13~",
		"\x1b[13;5u", "\x1b[13;5:1u", "\x1b[27;5;13~",
		"\x1b[13;6u", "\x1b[13;6:1u", "\x1b[27;6;13~":
		event := editorEvent(chatEditorText, "\n")
		return &event
	case "\x1b[106;5u", "\x1b[106;5:1u", "\x1b[27;5;106~",
		"\x1b[106;6u", "\x1b[106;6:1u", "\x1b[27;6;106~":
		event := editorEvent(chatEditorText, "\n")
		return &event
	case "\x1b[99;5u", "\x1b[99;5:1u", "\x1b[27;5;99~":
		return &chatTerminalKeyEvent{interrupt: true}
	case "\x1b[118;5u", "\x1b[118;5:1u", "\x1b[27;5;118~":
		event := editorEvent(chatEditorClipboardImage)
		return &event
	case "\x1b[9;2u", "\x1b[9;2:1u", "\x1b[27;2;9~":
		event := editorEvent(chatEditorTab)
		return &event
	}
	var key chatEditorKey
	switch sequence {
	case "\x1b[A", "\x1bOA", "\x1b[1;2A":
		key = chatEditorUp
	case "\x1b[B", "\x1bOB", "\x1b[1;2B":
		key = chatEditorDown
	case "\x1b[C", "\x1bOC", "\x1b[1;2C":
		key = chatEditorRight
	case "\x1b[D", "\x1bOD", "\x1b[1;2D":
		key = chatEditorLeft
	case "\x1b[H", "\x1b[1~", "\x1bOH", "\x1b[7~":
		key = chatEditorHome
	case "\x1b[F", "\x1bOF", "\x1b[8~":
		key = chatEditorEnd
	case "\x1b[3~":
		key = chatEditorDelete
	case "\x1b[Z":
		key = chatEditorTab
	case "\x1b[5;3~":
		return &chatTerminalKeyEvent{paneScroll: -chatDiffPaneScrollStep}
	case "\x1b[6;3~":
		return &chatTerminalKeyEvent{paneScroll: chatDiffPaneScrollStep}
	case "\x1b[5~":
		return &chatTerminalKeyEvent{scroll: 1}
	case "\x1b[6~":
		return &chatTerminalKeyEvent{scroll: -1}
	case "\x1b[4~":
		key = chatEditorEnd
	case "\x1b[1;5F":
		return &chatTerminalKeyEvent{returnToLive: true}
	default:
		return nil
	}
	event := editorEvent(key)
	return &event
}

func chatTerminalMouseEvent(sequence string) *chatTerminalKeyEvent {
	if !strings.HasPrefix(sequence, "\x1b[<") || len(sequence) < 4 {
		return nil
	}
	event := &chatTerminalKeyEvent{}
	if sequence[len(sequence)-1] != 'M' {
		return event
	}
	parts := strings.Split(sequence[3:len(sequence)-1], ";")
	if len(parts) != 3 {
		return event
	}
	button, err := strconv.Atoi(parts[0])
	if err != nil || button&64 == 0 {
		return event
	}
	if button&1 == 0 {
		event.scrollRows = 3
	} else {
		event.scrollRows = -3
	}
	return event
}

func (p *chatTerminalKeyParser) consumePaste() []chatTerminalKeyEvent {
	end := []byte(chatTerminalPasteEnd)
	index := bytes.Index(p.buffer, end)
	if index >= 0 {
		p.addPasteBytes(p.buffer[:index])
		p.buffer = p.buffer[index+len(end):]
		p.paste = false
		text := strings.ReplaceAll(string(p.pasteBytes), "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		event := editorEvent(chatEditorPaste, text)
		event.limitReached = p.pasteCapped
		p.pasteTail = p.pasteTail[:0]
		return []chatTerminalKeyEvent{event}
	}
	keep := len(end) - 1
	if len(p.buffer) <= keep {
		return nil
	}
	consume := len(p.buffer) - keep
	p.addPasteBytes(p.buffer[:consume])
	p.buffer = p.buffer[consume:]
	return nil
}

func (p *chatTerminalKeyParser) addPasteBytes(data []byte) {
	remaining := p.maxPaste + 1 - len(p.pasteBytes)
	if remaining <= 0 {
		p.pasteCapped = true
		return
	}
	if len(data) > remaining {
		data = data[:remaining]
		p.pasteCapped = true
	}
	p.pasteBytes = append(p.pasteBytes, data...)
}

func editorEvent(key chatEditorKey, text ...string) chatTerminalKeyEvent {
	input := chatEditorInput{Key: key}
	if len(text) > 0 {
		input.Text = text[0]
	}
	return chatTerminalKeyEvent{input: &input}
}

// chatFunctionKey returns N for an F1-F6 key sequence, or 0. Terminals encode
// these keys differently: SS3 P-S (xterm, tmux), CSI 11-17 ~ (VT220), and,
// with the kitty keyboard protocol Jara enables, CSI P, Q, S and CSI 13 ~ for
// F1-F4. Modified keys add ";MODIFIERS" (CSI 1;2Q, CSI 13;5~) and kitty may
// append ":EVENT"; any modifier still selects the key.
func chatFunctionKey(sequence string) int {
	if len(sequence) == 3 && strings.HasPrefix(sequence, "\x1bO") {
		return strings.IndexByte("PQRS", sequence[2]) + 1
	}
	body, found := strings.CutPrefix(sequence, "\x1b[")
	if !found || body == "" {
		return 0
	}
	final := body[len(body)-1]
	params := strings.Split(body[:len(body)-1], ";")
	if len(params) > 2 {
		return 0
	}
	for _, param := range params {
		if strings.Trim(param, "0123456789:") != "" {
			return 0
		}
	}
	if final == '~' {
		switch params[0] {
		case "11", "12", "13", "14", "15":
			return int(params[0][1] - '0')
		case "17":
			return 6
		}
		return 0
	}
	// CSI P/Q/S with no parameter or "1": the kitty form of F1, F2 and F4.
	// CSI R is a cursor position report unless it carries modifiers.
	if params[0] != "" && params[0] != "1" || final == 'R' && len(params) < 2 {
		return 0
	}
	return strings.IndexByte("PQRS", final) + 1
}
