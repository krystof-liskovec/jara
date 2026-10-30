package jara

import (
	"bytes"
	"testing"
)

func TestCodexColorQueriesHandleFragmentedInputAndPreserveOtherOutput(t *testing.T) {
	input := []byte("before\x1b]10;?\x1b\\middle\x1b]11;?\x07\x1b]0;title\x07after")
	for chunkSize := 1; chunkSize <= len(input); chunkSize++ {
		var output bytes.Buffer
		var answers []byte
		filter := codexColorQueries{answer: func(slot byte) error {
			answers = append(answers, slot)
			return nil
		}}
		for start := 0; start < len(input); start += chunkSize {
			end := min(start+chunkSize, len(input))
			part, err := filter.feed(input[start:end])
			if err != nil {
				t.Fatal(err)
			}
			output.Write(part)
		}
		output.Write(filter.finish())
		if got, want := output.String(), "beforemiddle\x1b]0;title\x07after"; got != want {
			t.Fatalf("chunk size %d output = %q, want %q", chunkSize, got, want)
		}
		if got, want := string(answers), string([]byte{10, 11}); got != want {
			t.Fatalf("chunk size %d answers = %v, want %v", chunkSize, answers, []byte{10, 11})
		}
	}
}

func TestCodexColorResponseUsesExpectedRGB(t *testing.T) {
	color := terminalRGB{208, 128, 32}
	if got, want := string(color.oscResponse(11)), "\x1b]11;rgb:d0d0/8080/2020\x1b\\"; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}

func TestTerminalBackgroundAcceptsConfiguredColor(t *testing.T) {
	t.Setenv("JARA_TMUX_BACKGROUND", "#f5f5f5")
	got, err := terminalBackground()
	if err != nil {
		t.Fatal(err)
	}
	if got.hex() != "#f5f5f5" || got.foreground() != (terminalRGB{32, 32, 32}) {
		t.Fatalf("unexpected colors: background %s foreground %v", got.hex(), got.foreground())
	}
}

func TestTerminalBackgroundRejectsMalformedColor(t *testing.T) {
	t.Setenv("JARA_TMUX_BACKGROUND", "#zzzzzz")
	if _, err := terminalBackground(); err == nil {
		t.Fatal("malformed color was accepted")
	}
}

func TestParseOSCBackgroundResponses(t *testing.T) {
	for _, response := range []string{
		"\x1b]11;rgb:1212/3434/5656\x07",
		"\x1b]11;rgb:12/34/56\x1b\\",
	} {
		got, ok := parseOSCBackground([]byte(response))
		if !ok || got.hex() != "#123456" {
			t.Fatalf("parse %q = %s, %t", response, got.hex(), ok)
		}
	}
	if _, ok := parseOSCBackground([]byte("\x1b]11;rgb:12/34/56")); ok {
		t.Fatal("accepted incomplete OSC response")
	}
}
