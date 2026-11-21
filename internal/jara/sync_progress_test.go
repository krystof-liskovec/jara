package jara

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestProgressReaderReportsShareRead(t *testing.T) {
	var reported []float64
	p := &progressReader{r: strings.NewReader("abcdefgh"), total: 8, report: func(f float64) { reported = append(reported, f) }}
	buf := make([]byte, 2)
	for {
		if _, err := p.Read(buf); err == io.EOF {
			break
		}
	}
	if len(reported) == 0 || reported[len(reported)-1] != 1 {
		t.Fatalf("reported %v, want ending at 1", reported)
	}
}

func TestSyncRowFollowsUploadAndFinishes(t *testing.T) {
	var out bytes.Buffer
	board := &upgradeBoard{out: &out, live: true, width: func() int { return 100 }}
	row := syncRow{board.row("push jellyfish")}
	row.stage("Uploading 2 file(s), 1.0 KiB", 0.9, time.Hour)
	row.advance(0.5)
	if got := row.target(); got < 0.44 || got > 0.46 {
		t.Fatalf("target %.3f, want halfway through the upload stage", got)
	}
	row.done("Transferred 2 file(s), deleted 0 previously synchronized file(s).", false)
	board.close()
	if !strings.Contains(out.String(), "Transferred 2 file(s)") || row.target() != 1 {
		t.Fatalf("row did not finish: %q", out.String())
	}
}

func TestTransferExpectationScalesWithSize(t *testing.T) {
	if transferExpectation(0) != time.Second {
		t.Fatal("small transfers should expect one second")
	}
	if transferExpectation(40<<20) < 5*time.Second {
		t.Fatal("large transfers should expect longer")
	}
}

func TestProgressBoardUsesJaraPalette(t *testing.T) {
	palette := menuColorsForBackground(terminalRGB{40, 40, 40})
	board := &upgradeBoard{out: &bytes.Buffer{}, live: true, color: true, theme: upgradeThemeFor(palette), width: func() int { return 100 }}
	row := board.row("push jellyfish")
	row.stage("Uploading", 0.9, time.Hour)
	row.shown = 0.5
	running := board.renderRow(row, 14, 100)
	row.finish("Transferred 1 file(s)")
	finished := board.renderRow(row, 14, 100)
	if !strings.Contains(running, "38;2;"+palette.accent) || !strings.Contains(finished, "38;2;"+palette.green) {
		t.Fatalf("rows not painted with the Jara palette:\n%q\n%q", running, finished)
	}
}
