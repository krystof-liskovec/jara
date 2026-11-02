//go:build linux || darwin

package jara

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

func TestMenuBackgroundProbeHelper(t *testing.T) {
	if os.Getenv("JARA_BACKGROUND_PROBE_HELPER") != "1" {
		return
	}
	first := menuColorsForTerminal()
	second := menuColorsForTerminal()
	fmt.Printf("\r\nRESULT:%s:%s\r\n", first.canvas, second.canvas)
}

func TestMenuReadsTerminalBackgroundOnce(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestMenuBackgroundProbeHelper$")
	command.Env = append(os.Environ(), "JARA_BACKGROUND_PROBE_HELPER=1", "JARA_THEME=", "TERM=xterm-256color", "NO_COLOR=")
	terminal, err := pty.StartWithSize(command, &pty.Winsize{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	defer command.Process.Kill()
	chunks := make(chan string, 32)
	go func() {
		defer close(chunks)
		var buf [512]byte
		for {
			n, e := terminal.Read(buf[:])
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if e != nil {
				return
			}
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	output := ""
	responded := false
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				t.Fatalf("probe helper closed: %q", output)
			}
			output += chunk
			if !responded && strings.Contains(output, "\x1b]11;?\x1b\\") {
				_, err = terminal.Write([]byte("\x1b]11;rgb:8080/9090/a0a0\x07"))
				if err != nil {
					t.Fatal(err)
				}
				responded = true
			}
			if strings.Contains(output, "RESULT:128;144;160:128;144;160") {
				if err = command.Wait(); err != nil {
					t.Fatal(err)
				}
				if strings.Count(output, "\x1b]11;?\x1b\\") != 1 {
					t.Fatal("nested menu queried terminal again")
				}
				return
			}
		case <-timer.C:
			t.Fatalf("terminal color query timed out: %q", output)
		}
	}
}
