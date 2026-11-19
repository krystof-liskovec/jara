package jara

import (
	"strings"

	"github.com/rivo/uniseg"
)

type chatVimMode string

const (
	chatVimOff    chatVimMode = ""
	chatVimInsert chatVimMode = "INSERT"
	chatVimNormal chatVimMode = "NORMAL"
)

func (e *chatEditor) vimCommand(args string) string {
	mode := e.vim
	switch strings.ToLower(args) {
	case "":
		mode = chatVimOff
		if e.vim == chatVimOff {
			mode = chatVimInsert
		}
	case "on":
		if e.vim == chatVimOff {
			mode = chatVimInsert
		}
	case "off":
		mode = chatVimOff
	case "status":
	default:
		return "Use /vim [on|off|status]."
	}
	if mode != e.vim {
		e.vim, e.vimDeletePending = mode, false
	}
	if mode == chatVimOff {
		return "Vim mode is off."
	}
	return "Vim mode is on (" + string(mode) + ")."
}

// vimEscape handles a switch to Normal mode or cancellation of a pending d.
func (e *chatEditor) vimEscape() bool {
	switch {
	case e.vim == chatVimInsert:
		e.Apply(chatEditorInput{Key: chatEditorEscape})
		e.vim = chatVimNormal
		if e.cursor > e.lineStart() {
			e.moveLeft()
		}
	case e.vimDeletePending:
		e.vimDeletePending = false
	default:
		return false
	}
	return true
}

func (e *chatEditor) applyVimNormal(key string) chatEditorResult {
	cursor, length, mode := e.cursor, len(e.text), e.vim
	if e.vimDeletePending {
		e.vimDeletePending = false
		if key == "d" {
			// Include the line break after the line, or before the final line.
			start, end := e.lineStart(), e.lineEnd()
			if end < len(e.text) {
				end++
			} else if start > 0 {
				start--
			}
			e.deleteRange(start, end)
			e.clampVimCursor()
			return chatEditorResult{Changed: cursor != e.cursor || length != len(e.text)}
		}
	}
	switch key {
	case "i":
		e.vim = chatVimInsert
	case "a":
		if e.cursor < e.lineEnd() {
			e.moveRight()
		}
		e.vim = chatVimInsert
	case "I":
		start := e.lineStart()
		line := e.text[start:e.lineEnd()]
		e.moveCursorTo(start + len(line) - len(strings.TrimLeft(line, " \t")))
		e.vim = chatVimInsert
	case "A":
		e.moveEnd()
		e.vim = chatVimInsert
	case "h":
		if e.cursor > e.lineStart() {
			e.moveLeft()
		}
	case "l":
		if e.cursor < e.lineEnd() {
			e.moveRight()
		}
	case "j":
		e.moveVimLine(1)
	case "k":
		e.moveVimLine(-1)
	case "w":
		e.moveWordRight()
	case "b":
		e.moveWordLeft()
	case "0":
		e.moveHome()
	case "$":
		e.moveEnd()
	case "x":
		if e.cursor < e.lineEnd() {
			e.deleteForward()
		}
	case "D":
		e.deleteRange(e.cursor, e.lineEnd())
	case "d":
		e.vimDeletePending = true
	default:
		return chatEditorResult{}
	}
	if e.vim == chatVimNormal {
		e.clampVimCursor()
	}
	return chatEditorResult{Changed: cursor != e.cursor || length != len(e.text) || mode != e.vim}
}

func (e *chatEditor) clampVimCursor() {
	if e.cursor == e.lineEnd() && e.cursor > e.lineStart() {
		e.moveLeft()
	}
}

func (e *chatEditor) moveVimLine(direction int) {
	start, end := e.lineStart(), e.lineEnd()
	column := uniseg.StringWidth(e.text[start:e.cursor])
	if direction < 0 {
		if start == 0 {
			return
		}
		end = start - 1
		start = strings.LastIndexByte(e.text[:end], '\n') + 1
	} else {
		if end == len(e.text) {
			return
		}
		start, end = end+1, len(e.text)
		if next := strings.IndexByte(e.text[start:], '\n'); next >= 0 {
			end = start + next
		}
	}
	graphemes := uniseg.NewGraphemes(e.text[start:end])
	target := start
	for graphemes.Next() && column >= graphemes.Width() {
		_, offset := graphemes.Positions()
		target = start + offset
		column -= graphemes.Width()
	}
	e.moveCursorTo(target)
}

func (u *chatUI) runChatVimCommand(args string) error {
	if u.vimCommand == nil {
		u.print("Vim mode is available in the full-screen conversation interface.\n")
		return nil
	}
	u.printRole("system", u.vimCommand(args)+"\n")
	return nil
}
