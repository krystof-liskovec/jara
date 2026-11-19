package jara

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rivo/uniseg"
)

func TestBuildStepWriterTracksBuildKitAndLegacySteps(t *testing.T) {
	board := newUpgradeBoard(&bytes.Buffer{})
	row := board.row("work")
	row.stage("Building image", 0.8, time.Minute)
	w := row.commandOutput()
	fmt.Fprint(w, "#5 [build 2/4] RUN go build\n#9 [stage-3 1/4] FROM node\r")
	if row.sub != 0.5 { // a newly seen stage never moves the bar back
		t.Fatalf("BuildKit progress = %v, want 1/2", row.sub)
	}
	fmt.Fprint(w, "#9 [stage-3 3/4] RUN npm\n#10 [build 1/4] CACHED\n")
	if row.sub != 5.0/8 {
		t.Fatalf("progress moved back to %v", row.sub)
	}
	row.stage("Testing image", 0.86, time.Second)
	fmt.Fprint(w, "Step 3/4 : RUN make\n")
	if row.sub != 0.75 || row.from < 0.8 {
		t.Fatalf("legacy progress = %v from %v", row.sub, row.from)
	}
}

func TestUpgradeBoardPrintsStagesWithoutTerminal(t *testing.T) {
	var out bytes.Buffer
	board := newUpgradeBoard(&out)
	work, home := board.row("work"), board.row("home")
	board.start()
	work.stage("Building image", 0.8, time.Minute)
	fmt.Fprint(work, "WARNING: something\npartial")
	work.finish("Jara 1.0.0 → 1.1.0")
	home.settle(errors.New("build failed\nlast output"))
	board.close()
	want := "work: Building image…\nwork: WARNING: something\nwork: partial\nwork: Jara 1.0.0 → 1.1.0\nhome: failed: build failed\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestUpgradeBoardRedrawsRowsInPlace(t *testing.T) {
	var out bytes.Buffer
	board := &upgradeBoard{out: &out, live: true, width: func() int { return 60 }}
	work, longer := board.row("work"), board.row("longer-name")
	work.stage("Building image with a label far too long to fit", 0.8, time.Minute)
	board.mu.Lock()
	work.shown = 0.5
	board.drawLocked()
	board.drawLocked()
	board.mu.Unlock()
	fmt.Fprintln(longer, "WARNING: kept above the bars")
	frames := out.String()
	if strings.Count(frames, "\x1b[2F") < 2 || !strings.Contains(frames, "\x1b[2F\x1b[J"+"longer-name: WARNING: kept above the bars\n") {
		t.Fatalf("rows not redrawn in place: %q", frames)
	}
	line := board.renderRow(work, 11, 60)
	if w := uniseg.StringWidth(line); w > 60 {
		t.Fatalf("row is %d columns wide: %q", w, line)
	}
	if !strings.Contains(line, "work         ") || !strings.Contains(line, " 50%") || !strings.Contains(line, "…") {
		t.Fatalf("unexpected row %q", line)
	}
}

func TestOutputTailKeepsLastLines(t *testing.T) {
	var sink bytes.Buffer
	tail := &outputTail{out: &sink}
	for i := range outputTailLines + 5 {
		fmt.Fprintf(tail, "line %d\n", i)
	}
	fmt.Fprint(tail, "unterminated")
	text := tail.text()
	if strings.Contains(text, "line 4\n") || !strings.HasPrefix(text, "line 5\n") || !strings.HasSuffix(text, "unterminated") {
		t.Fatalf("tail = %q", text)
	}
	if !strings.Contains(sink.String(), "line 0\n") {
		t.Fatal("output was not forwarded")
	}
}
