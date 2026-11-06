package jara

import (
	"unicode"

	"github.com/rivo/uniseg"
)

type chatEditorWordGrapheme struct {
	start, end int
	word       bool
}

func (e *chatEditor) moveWordLeft() bool {
	graphemes := chatEditorWordGraphemes(e.text)
	index := -1
	for i, grapheme := range graphemes {
		if grapheme.end > e.cursor {
			break
		}
		index = i
	}
	for index >= 0 && !graphemes[index].word {
		index--
	}
	target := 0
	if index >= 0 {
		for index > 0 && graphemes[index-1].word {
			index--
		}
		target = graphemes[index].start
	}
	return e.moveCursorTo(target)
}

func (e *chatEditor) moveWordRight() bool {
	graphemes := chatEditorWordGraphemes(e.text)
	index := 0
	for index < len(graphemes) && graphemes[index].end <= e.cursor {
		index++
	}
	if index == len(graphemes) {
		return false
	}
	if graphemes[index].word {
		for index < len(graphemes) && graphemes[index].word {
			index++
		}
	}
	for index < len(graphemes) && !graphemes[index].word {
		index++
	}
	target := len(e.text)
	if index < len(graphemes) {
		target = graphemes[index].start
	}
	return e.moveCursorTo(target)
}

func chatEditorWordGraphemes(text string) []chatEditorWordGrapheme {
	var result []chatEditorWordGrapheme
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		start, end := graphemes.Positions()
		word := false
		for _, r := range graphemes.Str() {
			if r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) {
				word = true
				break
			}
		}
		result = append(result, chatEditorWordGrapheme{start: start, end: end, word: word})
	}
	return result
}

func (e *chatEditor) moveCursorTo(target int) bool {
	if e.cursor == target {
		return false
	}
	e.cursor = target
	e.afterCursorMove()
	return true
}
