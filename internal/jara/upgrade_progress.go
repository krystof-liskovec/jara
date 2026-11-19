package jara

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

// upgradeReporter receives what a deployment upgrade does. The default draws
// an animated progress bar; --verbose prints every command's output instead.
type upgradeReporter interface {
	io.Writer // messages worth keeping on screen, such as warnings
	detail(text string)
	stage(label string, to float64, expect time.Duration)
	commandOutput() io.Writer // nil streams remote commands to the terminal
	prompt(func())
	finish(summary string)
	skip(reason string)
}

// verboseUpgrade prints everything, as upgrades did before progress bars.
type verboseUpgrade struct{ out io.Writer }

func (v verboseUpgrade) Write(p []byte) (int, error)        { return v.out.Write(p) }
func (v verboseUpgrade) detail(text string)                 { fmt.Fprint(v.out, text) }
func (verboseUpgrade) stage(string, float64, time.Duration) {}
func (verboseUpgrade) commandOutput() io.Writer             { return nil }
func (verboseUpgrade) prompt(fn func())                     { fn() }
func (v verboseUpgrade) finish(summary string)              { fmt.Fprintf(v.out, "Upgraded %s.\n", summary) }
func (v verboseUpgrade) skip(reason string)                 {}

type upgradeRowState int

const (
	rowRunning upgradeRowState = iota
	rowDone
	rowSkipped
	rowFailed
)

const (
	upgradeBarWidth = 24
	upgradeTick     = 80 * time.Millisecond
)

var upgradeSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// upgradeBoard draws one progress row per deployment and redraws them in
// place while upgrades run in parallel. Messages print above the rows.
type upgradeBoard struct {
	mu       sync.Mutex
	promptMu sync.Mutex
	out      io.Writer
	live     bool
	color    bool
	width    func() int
	rows     []*upgradeRow
	drawn    int
	paused   bool
	frame    int
	stop     chan struct{}
	stopped  chan struct{}
}

// newUpgradeBoard animates when out is a terminal, and otherwise prints one
// line per stage.
func newUpgradeBoard(out io.Writer) *upgradeBoard {
	b := &upgradeBoard{out: out, width: func() int { return 80 }}
	if f, ok := out.(*os.File); ok && os.Getenv("TERM") != "dumb" && term.IsTerminal(int(f.Fd())) {
		b.live = true
		b.color = os.Getenv("NO_COLOR") == ""
		b.width = func() int {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
				return w
			}
			return 80
		}
	}
	return b
}

func (b *upgradeBoard) row(name string) *upgradeRow {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := &upgradeRow{board: b, name: name, label: "Waiting", sub: -1, started: time.Now(), since: time.Now(), expect: time.Second}
	b.rows = append(b.rows, r)
	return r
}

func (b *upgradeBoard) start() {
	if !b.live {
		return
	}
	b.stop, b.stopped = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(b.stopped)
		ticker := time.NewTicker(upgradeTick)
		defer ticker.Stop()
		for {
			select {
			case <-b.stop:
				return
			case <-ticker.C:
				b.mu.Lock()
				b.frame++
				for _, r := range b.rows {
					r.shown += (r.target() - r.shown) * 0.25
				}
				b.drawLocked()
				b.mu.Unlock()
			}
		}
	}()
}

// close stops the animation and leaves the final rows on screen.
func (b *upgradeBoard) close() {
	if b.stop != nil {
		close(b.stop)
		<-b.stopped
		b.stop = nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, r := range b.rows {
		r.shown = r.target()
	}
	b.drawLocked()
	b.drawn = 0
}

func (b *upgradeBoard) clearLocked() {
	if b.drawn > 0 {
		fmt.Fprintf(b.out, "\x1b[%dF\x1b[J", b.drawn)
		b.drawn = 0
	}
}

func (b *upgradeBoard) drawLocked() {
	if !b.live || b.paused {
		return
	}
	var frame bytes.Buffer
	if b.drawn > 0 {
		fmt.Fprintf(&frame, "\x1b[%dF", b.drawn)
	}
	nameWidth := 0
	for _, r := range b.rows {
		nameWidth = max(nameWidth, uniseg.StringWidth(r.name))
	}
	for _, r := range b.rows {
		frame.WriteString("\r\x1b[2K")
		frame.WriteString(b.renderRow(r, nameWidth, b.width()))
		frame.WriteByte('\n')
	}
	b.drawn = len(b.rows)
	_, _ = b.out.Write(frame.Bytes())
}

func (b *upgradeBoard) paint(code, text string) string {
	if !b.color || code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (b *upgradeBoard) renderRow(r *upgradeRow, nameWidth, width int) string {
	icon, tint := upgradeSpinner[b.frame%len(upgradeSpinner)], "36"
	switch r.state {
	case rowDone:
		icon, tint = "✓", "32"
	case rowSkipped:
		icon, tint = "–", "33"
	case rowFailed:
		icon, tint = "✗", "31"
	}
	fraction := min(max(r.shown, 0), 1)
	label := r.label
	if r.state != rowRunning {
		label = r.result
	}
	elapsed := formatUpgradeElapsed(r.elapsed())
	name := r.name + strings.Repeat(" ", nameWidth-uniseg.StringWidth(r.name))
	percent := fmt.Sprintf("%3d%%", int(math.Round(fraction*100)))
	fixed := 2 + 1 + 1 + nameWidth + 2 + upgradeBarWidth + 2 + len(percent) + 2 + 3 + len(elapsed) + 1
	label = truncateUpgradeLabel(label, width-fixed)
	line := "  " + b.paint(tint, icon) + " " + b.paint("1", name) + "  " + b.renderBar(r, fraction, tint) + "  " + percent + "  " + label
	if label != "" {
		line += b.paint("2", " · "+elapsed)
	}
	return line
}

// renderBar fills in half cells and, while running, sweeps a highlight along
// the filled part so the bar shows life during long build steps.
func (b *upgradeBoard) renderBar(r *upgradeRow, fraction float64, tint string) string {
	halves := int(fraction * upgradeBarWidth * 2)
	full, half := halves/2, halves%2 == 1
	var bar strings.Builder
	pulse := -1
	if r.state == rowRunning && full > 2 {
		pulse = b.frame % (full + 8)
	}
	for i := 0; i < full; i++ {
		if i == pulse || i == pulse-1 {
			bar.WriteString(b.paint("1;"+brightTint(tint), "━"))
		} else {
			bar.WriteString(b.paint(tint, "━"))
		}
	}
	rest := upgradeBarWidth - full
	if half {
		bar.WriteString(b.paint(tint, "╸"))
		rest--
	}
	track := "─"
	if b.color {
		track = "━"
	}
	bar.WriteString(b.paint("90", strings.Repeat(track, rest)))
	return bar.String()
}

func brightTint(code string) string {
	if n, err := strconv.Atoi(code); err == nil && n >= 30 && n <= 37 {
		return strconv.Itoa(n + 60)
	}
	return code
}

func truncateUpgradeLabel(label string, width int) string {
	if width <= 1 {
		return ""
	}
	if uniseg.StringWidth(label) <= width {
		return label
	}
	var out strings.Builder
	used := 0
	g := uniseg.NewGraphemes(label)
	for g.Next() {
		w := g.Width()
		if used+w > width-1 {
			break
		}
		out.WriteString(g.Str())
		used += w
	}
	return out.String() + "…"
}

func formatUpgradeElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// upgradeRow is one deployment's progress. Stages own a slice of the bar;
// inside a stage the bar follows known step counts or creeps toward its end.
type upgradeRow struct {
	board          *upgradeBoard
	name, label    string
	result         string
	from, to       float64
	sub, shown     float64
	expect         time.Duration
	since          time.Time
	started, ended time.Time
	state          upgradeRowState
	partial        []byte
	steps          map[string][2]int
}

func (r *upgradeRow) target() float64 {
	if r.state != rowRunning {
		if r.state == rowDone {
			return 1
		}
		return r.shown
	}
	within := r.sub
	if within < 0 {
		within = 0.9 * (1 - math.Exp(-float64(time.Since(r.since))/float64(r.expect)))
	}
	return r.from + (r.to-r.from)*within
}

func (r *upgradeRow) elapsed() time.Duration {
	if !r.ended.IsZero() {
		return r.ended.Sub(r.started)
	}
	return time.Since(r.started)
}

func (r *upgradeRow) stage(label string, to float64, expect time.Duration) {
	b := r.board
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.state != rowRunning {
		return
	}
	r.from = max(r.to, r.target())
	r.to, r.label, r.expect, r.since, r.sub, r.steps = to, label, max(expect, time.Second), time.Now(), -1, nil
	if !b.live {
		fmt.Fprintf(b.out, "%s: %s…\n", r.name, label)
	}
}

func (r *upgradeRow) Write(p []byte) (int, error) {
	b := r.board
	b.mu.Lock()
	defer b.mu.Unlock()
	r.partial = append(r.partial, p...)
	for {
		i := bytes.IndexByte(r.partial, '\n')
		if i < 0 {
			break
		}
		b.noteLocked(r, string(r.partial[:i]))
		r.partial = r.partial[i+1:]
	}
	return len(p), nil
}

func (b *upgradeBoard) noteLocked(r *upgradeRow, line string) {
	line = strings.TrimRight(line, "\r ")
	if strings.TrimSpace(line) == "" {
		return
	}
	if len(b.rows) > 1 || !b.live {
		line = r.name + ": " + line
	}
	b.clearLocked()
	fmt.Fprintln(b.out, line)
	b.drawLocked()
}

func (*upgradeRow) detail(string) {}

func (r *upgradeRow) commandOutput() io.Writer { return &buildStepWriter{row: r} }

// prompt hides the rows while fn talks to the user. Prompts from parallel
// upgrades take turns.
func (r *upgradeRow) prompt(fn func()) {
	b := r.board
	b.promptMu.Lock()
	defer b.promptMu.Unlock()
	b.mu.Lock()
	b.clearLocked()
	b.paused = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.paused = false
		b.drawLocked()
		b.mu.Unlock()
	}()
	fn()
}

func (r *upgradeRow) end(state upgradeRowState, result string) {
	b := r.board
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.state != rowRunning {
		return
	}
	if len(r.partial) > 0 {
		b.noteLocked(r, string(r.partial))
		r.partial = nil
	}
	r.state, r.result, r.ended = state, result, time.Now()
	if state == rowDone {
		r.shown = 1
	}
	if !b.live {
		fmt.Fprintf(b.out, "%s: %s\n", r.name, result)
	}
}

func (r *upgradeRow) finish(summary string) { r.end(rowDone, summary) }
func (r *upgradeRow) skip(reason string)    { r.end(rowSkipped, reason) }

// settle marks how an upgrade ended: failed with err, or skipped when it
// returned without finishing.
func (r *upgradeRow) settle(err error) {
	if err != nil {
		message, _, _ := strings.Cut(err.Error(), "\n")
		r.end(rowFailed, "failed: "+message)
		return
	}
	r.end(rowSkipped, "not upgraded")
}

var (
	buildKitStepRE = regexp.MustCompile(`\[\s*(?:([\w.-]+)\s+)?(\d+)/(\d+)\]`)
	legacyStepRE   = regexp.MustCompile(`^Step (\d+)/(\d+) :`)
)

// buildStepWriter turns Docker build step markers into progress within the
// current stage and discards the rest of the output.
type buildStepWriter struct {
	row     *upgradeRow
	partial []byte
}

func (w *buildStepWriter) Write(p []byte) (int, error) {
	w.partial = append(w.partial, p...)
	for {
		i := bytes.IndexAny(w.partial, "\r\n")
		if i < 0 {
			break
		}
		w.line(string(w.partial[:i]))
		w.partial = w.partial[i+1:]
	}
	return len(p), nil
}

func (w *buildStepWriter) line(line string) {
	stage, done, total := "", 0, 0
	if m := legacyStepRE.FindStringSubmatch(line); m != nil {
		done, _ = strconv.Atoi(m[1])
		total, _ = strconv.Atoi(m[2])
	} else if m := buildKitStepRE.FindStringSubmatch(line); m != nil {
		stage = m[1]
		done, _ = strconv.Atoi(m[2])
		total, _ = strconv.Atoi(m[3])
	}
	if total <= 0 || done > total {
		return
	}
	r := w.row
	b := r.board
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.steps == nil {
		r.steps = map[string][2]int{}
	}
	if seen := r.steps[stage]; done > seen[0] {
		r.steps[stage] = [2]int{done, total}
	}
	sumDone, sumTotal := 0, 0
	for _, step := range r.steps {
		sumDone += step[0]
		sumTotal += step[1]
	}
	r.sub = max(r.sub, min(float64(sumDone)/float64(sumTotal), 0.98))
}
