//go:build linux || darwin

package jara

import (
	"bufio"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// A stopped chat UI must not keep reading the terminal: the next line belongs
// to whatever runs there next, such as the Jara that /remote reopens.
func TestStoppedChatTerminalReaderLeavesInputForNextReader(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skip("no pseudo-terminal:", err)
	}
	defer master.Close()
	defer slave.Close()
	state, err := term.MakeRaw(int(slave.Fd())) // as the chat UI runs
	if err != nil {
		t.Fatal(err)
	}
	chunks, errs := make(chan []byte, 1), make(chan error, 1)
	stop, err := startChatTerminalReader(slave, chunks, errs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = master.Write([]byte("a")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-chunks:
		if string(got) != "a" {
			t.Fatalf("reader got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not deliver input")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not end the reader")
	}
	if err = term.Restore(int(slave.Fd()), state); err != nil {
		t.Fatal(err)
	}
	if _, err = master.Write([]byte("y\n")); err != nil {
		t.Fatal(err)
	}
	line := make(chan string, 1)
	go func() {
		s, _ := bufio.NewReader(slave).ReadString('\n')
		line <- s
	}()
	select {
	case got := <-line:
		if got != "y\n" {
			t.Fatalf("next reader got %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("next reader never got the line")
	}
	select {
	case chunk := <-chunks:
		t.Fatalf("stopped reader consumed %q", chunk)
	default:
	}
}
