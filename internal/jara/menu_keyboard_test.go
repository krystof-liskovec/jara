//go:build linux || darwin

package jara

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestMenuKeyboardHelper(t *testing.T) {
	if os.Getenv("JARA_MENU_KEY_HELPER") != "1" {
		return
	}
	items := []menuItem{{Title: "Alpha"}, {Title: "Beta"}}
	n, _, err := terminalMenu("Child", items)
	result := "open"
	if errors.Is(err, ErrUIQuit) {
		result = "quit"
	} else if errors.Is(err, io.EOF) {
		result = "back"
	} else if err != nil {
		result = "error"
	}
	fmt.Printf("\r\nRESULT:%d:%s\r\n", n, result)
}

func TestMenuKeyboardNavigation(t *testing.T) {
	for _, test := range []struct{ name, keys, want string }{
		{"right opens", "\x1b[B\x1b[C", "RESULT:1:open"},
		{"application arrows", "\x1bOB\x1bOC", "RESULT:1:open"},
		{"left backs", "\x1b[D", "RESULT:0:back"},
		{"application left", "\x1bOD", "RESULT:0:back"},
		{"delete backs", "\x7f", "RESULT:0:back"},
		{"backspace backs", "\x08", "RESULT:0:back"},
		{"search edits then backs", "/a\x7f\x7f", "RESULT:0:back"},
		{"left leaves search", "/a\x1b[D", "RESULT:0:back"},
		{"ctrl c exits", "\x03", "RESULT:0:quit"},
		{"ctrl c exits search", "/a\x03", "RESULT:0:quit"},
		{"ctrl c during escape", "\x1b\x03", "RESULT:0:quit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMenuKeyboardHelper$")
			cmd.Env = append(os.Environ(), "JARA_MENU_KEY_HELPER=1", "TERM=xterm-256color", "NO_COLOR=1")
			terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 80, Rows: 24})
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			defer cmd.Process.Kill()
			chunks := make(chan string, 64)
			go func() {
				defer close(chunks)
				var buffer [4096]byte
				for {
					n, err := terminal.Read(buffer[:])
					if n > 0 {
						chunks <- string(buffer[:n])
					}
					if err != nil {
						return
					}
				}
			}()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			output := ""
			sent := false
			for {
				select {
				case part, ok := <-chunks:
					if !ok {
						t.Fatalf("helper closed: %s", output)
					}
					output += part
					if !sent && strings.Contains(output, "JARA") {
						if _, err = terminal.Write([]byte(test.keys)); err != nil {
							t.Fatal(err)
						}
						sent = true
					}
					if strings.Contains(output, test.want) {
						if err = cmd.Wait(); err != nil {
							t.Fatal(err)
						}
						return
					}
				case <-timer.C:
					t.Fatalf("key handling timed out: %s", output)
				}
			}
		})
	}
}

func TestMenuQuitDoesNotMeanBack(t *testing.T) {
	if errors.Is(ErrUIQuit, io.EOF) {
		t.Fatal("quit must propagate past parent menus")
	}
	err := exec.Command("sh", "-c", "exit 130").Run()
	if !errors.Is(remoteMenuResult(err), ErrUIQuit) {
		t.Fatal("remote quit was not propagated")
	}
}
