package jara

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

func readSetupPathLine(reader *bufio.Reader, fd int, output io.Writer, prompt string) (string, bool, error) {
	if !term.IsTerminal(fd) {
		return "", false, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", false, nil
	}
	defer term.Restore(fd, state)

	input, cursor := "", 0
	redraw := func() {
		fmt.Fprintf(output, "\r%s%s\x1b[K", prompt, input)
		if cursor < len(input) {
			fmt.Fprintf(output, "\x1b[%dD", utf8.RuneCountInString(input[cursor:]))
		}
	}
	for {
		key, err := reader.ReadByte()
		if err != nil {
			fmt.Fprint(output, "\r\n")
			if err == io.EOF && input != "" {
				return input, true, nil
			}
			return "", true, err
		}
		switch key {
		case '\r', '\n':
			fmt.Fprint(output, "\r\n")
			return input, true, nil
		case 3:
			fmt.Fprint(output, "\r\n")
			return "", true, fmt.Errorf("input interrupted")
		case 4:
			fmt.Fprint(output, "\r\n")
			if input == "" {
				return "", true, io.EOF
			}
			return input, true, nil
		case 8, 127:
			if cursor > 0 {
				_, size := utf8.DecodeLastRuneInString(input[:cursor])
				input = input[:cursor-size] + input[cursor:]
				cursor -= size
				redraw()
			}
		case 9:
			if cursor != len(input) {
				fmt.Fprint(output, "\a")
				continue
			}
			matches, completionErr := setupPathCompletions(input)
			if completionErr != nil || len(matches) == 0 {
				fmt.Fprint(output, "\a")
				continue
			}
			if len(matches) == 1 {
				input = matches[0]
				cursor = len(input)
				redraw()
				continue
			}
			common := commonSetupPathPrefix(matches)
			if len(common) > len(input) {
				input = common
				cursor = len(input)
				redraw()
				continue
			}
			fmt.Fprint(output, "\r\n")
			for _, match := range matches {
				name := filepath.Base(strings.TrimRight(match, setupPathSeparators()))
				fmt.Fprintf(output, "  %s\r\n", name)
			}
			redraw()
		case 21:
			input, cursor = "", 0
			redraw()
		case 27:
			switch readSetupPathEscape(reader) {
			case "left":
				if cursor > 0 {
					_, size := utf8.DecodeLastRuneInString(input[:cursor])
					cursor -= size
					redraw()
				}
			case "right":
				if cursor < len(input) {
					_, size := utf8.DecodeRuneInString(input[cursor:])
					cursor += size
					redraw()
				}
			case "home":
				cursor = 0
				redraw()
			case "end":
				cursor = len(input)
				redraw()
			case "delete":
				if cursor < len(input) {
					_, size := utf8.DecodeRuneInString(input[cursor:])
					input = input[:cursor] + input[cursor+size:]
					redraw()
				}
			}
		default:
			if key >= 32 {
				input = input[:cursor] + string([]byte{key}) + input[cursor:]
				cursor++
				redraw()
			}
		}
	}
}

func setupPathCompletions(input string) ([]string, error) {
	separatorChars := setupPathSeparators()
	if input == "~" {
		return []string{"~" + string(os.PathSeparator)}, nil
	}
	index := strings.LastIndexAny(input, separatorChars)
	directoryPrefix, prefix := "", input
	separator := string(os.PathSeparator)
	if index >= 0 {
		directoryPrefix, prefix = input[:index+1], input[index+1:]
		separator = input[index : index+1]
	}
	directory := strings.TrimRight(directoryPrefix, separatorChars)
	if directory == "" {
		if directoryPrefix != "" {
			directory = string(os.PathSeparator)
		} else {
			directory = "."
		}
	}
	if directory == "~" || strings.HasPrefix(directory, "~/") || strings.HasPrefix(directory, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		if directory == "~" {
			directory = home
		} else {
			directory = filepath.Join(home, directory[2:])
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	matches := make([]string, 0)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || prefix == "" && strings.HasPrefix(name, ".") {
			continue
		}
		match := directoryPrefix + name
		if entry.IsDir() {
			match += separator
		}
		matches = append(matches, match)
	}
	return matches, nil
}

func commonSetupPathPrefix(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	common := matches[0]
	for _, match := range matches[1:] {
		limit := min(len(common), len(match))
		i := 0
		for i < limit && common[i] == match[i] {
			i++
		}
		common = common[:i]
	}
	for !utf8.ValidString(common) {
		common = common[:len(common)-1]
	}
	return common
}

func setupPathSeparators() string {
	separators := string(os.PathSeparator)
	if os.PathSeparator != '/' {
		separators += "/"
	}
	return separators
}

func readSetupPathEscape(reader *bufio.Reader) string {
	first, err := reader.ReadByte()
	if err != nil {
		return ""
	}
	if first == 'O' {
		last, _ := reader.ReadByte()
		return setupPathEscapeAction(last)
	}
	if first != '[' {
		_ = reader.UnreadByte()
		return ""
	}
	sequence := make([]byte, 0, 4)
	for len(sequence) < 8 {
		key, readErr := reader.ReadByte()
		if readErr != nil {
			return ""
		}
		sequence = append(sequence, key)
		if key >= 0x40 && key <= 0x7e {
			break
		}
	}
	if len(sequence) == 1 {
		return setupPathEscapeAction(sequence[0])
	}
	if len(sequence) == 2 && sequence[0] == '3' && sequence[1] == '~' {
		return "delete"
	}
	if len(sequence) == 2 && sequence[1] == '~' {
		if sequence[0] == '1' || sequence[0] == '7' {
			return "home"
		}
		if sequence[0] == '4' || sequence[0] == '8' {
			return "end"
		}
	}
	return ""
}

func setupPathEscapeAction(key byte) string {
	switch key {
	case 'D':
		return "left"
	case 'C':
		return "right"
	case 'H':
		return "home"
	case 'F':
		return "end"
	default:
		return ""
	}
}
