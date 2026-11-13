//go:build linux || darwin

package jara

import (
	"fmt"
	"strconv"
	"strings"
)

// handleKey applies one key or paste event; done means the interface exits.
func (t *chatTerminal) handleKey(event chatTerminalKeyEvent) (done bool, err error) {
	u := t.u
	if u.diff.fullScreen() && t.handleFullScreenDiffKey(event) {
		return false, nil
	}
	switch {
	case event.scrollRows != 0:
		t.scroll(t.scrollOffset + event.scrollRows)
		return false, nil
	case event.scroll != 0:
		t.scroll(t.scrollOffset + event.scroll*max(1, heightForChatTerminal(t.output)/2))
		return false, nil
	case event.diffPane || event.paneScroll != 0:
		handleChatPaneKey(&u.diff, event, t.output)
		t.dirty = true
		return false, nil
	case event.returnToLive:
		t.scroll(0)
		return false, nil
	case event.agentStep != 0:
		t.showAgent(func() { u.cycleChatAgentView(event.agentStep) })
		return false, nil
	case event.interrupt:
		return t.interrupt()
	case event.tap != nil:
		return t.tap(event.tap[0], event.tap[1])
	case event.input == nil:
		return false, nil
	}
	input := event.input
	switch input.Key {
	case chatEditorModelPicker:
		t.openPicker("model")
		return false, nil
	case chatEditorReasoningPicker:
		t.openPicker("reasoning")
		return false, nil
	}
	chatTerminalAttachImages(u, input)
	if answer := chatApprovalKey(u, t.editor, *input); answer != "" {
		t.dirty = true
		return u.line(answer)
	}
	if t.handlePickerKey(*input) || t.handleSessionPickerKey(*input) {
		t.dirty = true
		return false, nil
	}
	if t.queueEditor.active() && input.Key == chatEditorEscape {
		t.queueEditor.cancel(u, t.editor)
		t.scroll(0)
		return false, nil
	}
	if t.queueOnTab(*input) {
		t.dirty = true
		return false, nil
	}
	result := t.editor.Apply(*input)
	if result.Changed {
		u.noteChatComposer(t.editor.Text(), t.editor.CursorByte())
	}
	if result.LimitReached || event.limitReached {
		t.transcript.append("system", "Input reached the size limit; the remaining text was omitted.\n")
	}
	t.dirty = t.dirty || result.Changed || result.LimitReached || event.limitReached
	if !result.Submitted {
		return false, nil
	}
	return t.submit(result.Value)
}

func (t *chatTerminal) scroll(offset int) {
	t.scrollOffset, t.dirty = max(0, offset), true
}

func (t *chatTerminal) openPicker(kind string) {
	t.sessionPicker, t.resumePickerPending = false, false
	if err := t.picker.request(t.u, kind); err != nil {
		t.u.pickerRequested = ""
		t.transcript.append("system", chatPickerErrorText("Could not open picker", err)+"\n")
	}
	t.dirty = true
}

// handlePickerKey drives an open model, reasoning, default, fallback, or
// rewind picker. Any other key closes it and goes on to the input box.
func (t *chatTerminal) handlePickerKey(input chatEditorInput) bool {
	if !t.picker.active() {
		return false
	}
	switch input.Key {
	case chatEditorUp:
		t.picker.move(t.u, -1)
	case chatEditorDown:
		t.picker.move(t.u, 1)
	case chatEditorEscape:
		t.picker.close()
	case chatEditorEnter:
		if err := t.picker.choose(t.u); err != nil {
			t.transcript.append("system", err.Error()+"\n")
		}
		t.applyPrefill()
	default:
		t.picker.close()
		return false
	}
	return true
}

func (t *chatTerminal) handleSessionPickerKey(input chatEditorInput) bool {
	if !t.sessionPicker {
		return false
	}
	u := t.u
	switch input.Key {
	case chatEditorUp:
		t.sessionSelection = max(0, t.sessionSelection-1)
	case chatEditorDown:
		if u.sessions != nil && len(u.sessions.matches) > 0 {
			t.sessionSelection = min(len(u.sessions.matches)-1, t.sessionSelection+1)
		}
	case chatEditorEscape:
		t.sessionPicker, t.resumePickerPending = false, false
	case chatEditorEnter:
		if u.busy {
			t.transcript.append("system", "A turn is running. Cancel or wait before opening a saved session.\n")
		} else if u.sessions != nil && u.sessions.complete && len(u.sessions.matches) > 0 {
			t.sessionPicker = false
			if err := u.resumeChatSessionChoice(strconv.Itoa(t.sessionSelection + 1)); err != nil {
				t.transcript.append("system", err.Error()+"\n")
			}
		}
	default:
		t.sessionPicker, t.resumePickerPending = false, false
		return false
	}
	return true
}

// queueOnTab queues the typed prompt with Tab while a turn runs.
func (t *chatTerminal) queueOnTab(input chatEditorInput) bool {
	u, editor := t.u, t.editor
	text := strings.TrimSpace(editor.Text())
	if t.queueEditor.active() || input.Key != chatEditorTab || !u.agentTurnActive() || !u.threadReady ||
		text == "" || strings.HasPrefix(text, "!") || len(editor.Completions()) > 0 {
		return false
	}
	if err := u.queuePrompt(u.thread, editor.Text()); err != nil {
		t.transcript.append("system", err.Error()+"\n")
	} else {
		t.transcript.append("system", fmt.Sprintf("Queued prompt %d. It will run after the current turn.\n", len(u.queuedPrompts)))
		editor.SetText("")
	}
	return true
}

// submit handles Enter: it saves an edited queued prompt or runs the line.
func (t *chatTerminal) submit(value string) (done bool, err error) {
	u, editor := t.u, t.editor
	defer t.scroll(0)
	if t.queueEditor.active() {
		t.queueEditor.save(u, editor, value)
		return false, nil
	}
	if sent, err := t.submitToAgent(value); sent || err != nil {
		return false, err
	}
	if line := strings.TrimSpace(value); line != "" {
		isCommand := strings.HasPrefix(line, "/") && !strings.HasPrefix(line, "//")
		wasBusy, wasReady, queuedOffline := u.busy, u.threadReady, u.chatOffline() && !isCommand
		busyControl := wasBusy && (line == "/new" || strings.HasPrefix(line, "/resume ") || line == "/compose")
		if line == "/resume" {
			t.picker.close()
			t.resumePickerPending, t.sessionPicker, t.sessionSelection = true, true, 0
		} else if strings.HasPrefix(line, "/sessions") {
			t.resumePickerPending, t.sessionPicker = false, false
		}
		previousSessions := u.sessions
		quit, err := u.line(value)
		if err != nil || quit {
			return true, err
		}
		t.applyAgentViewRequest()
		t.picker.sync(u)
		t.applyPrefill()
		if line == "/resume" && u.sessions == previousSessions {
			t.resumePickerPending, t.sessionPicker = false, false
		}
		if (!isCommand || busyControl) && (wasBusy || !wasReady) && !queuedOffline {
			editor.SetText(value) // not sent; keep it to send later
		}
	}
	if id := u.queueEditRequest; id != 0 {
		u.queueEditRequest = 0
		if !t.queueEditor.begin(u, editor, id) {
			t.transcript.append("system", "That queued prompt is no longer available to edit.\n")
		}
	}
	return false, nil
}
