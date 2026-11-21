package jara

import (
	"fmt"
	"io"
	"os"
	"time"
)

// syncReporter receives what a workspace sync does. The default stays quiet
// and prints notices to stderr; on a terminal, `jara sync` and the push
// before a session draw a progress bar like deployment upgrades.
type syncReporter interface {
	stage(label string, to float64, expect time.Duration)
	advance(fraction float64) // progress within the current stage, 0 to 1
	notices() io.Writer       // messages and command output worth keeping
	done(summary string, show bool)
}

// syncProgress reports the sync running in this process.
var syncProgress syncReporter = quietSync{}

type quietSync struct{}

func (quietSync) stage(string, float64, time.Duration) {}
func (quietSync) advance(float64)                      {}
func (quietSync) notices() io.Writer                   { return os.Stderr }
func (quietSync) done(summary string, show bool) {
	if show {
		fmt.Fprintln(os.Stderr, summary)
	}
}

// syncRow shows a sync as one upgrade board row.
type syncRow struct{ *upgradeRow }

func (r syncRow) advance(fraction float64) {
	b := r.board
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.state == rowRunning {
		r.sub = max(r.sub, min(fraction, 1))
	}
}

func (r syncRow) notices() io.Writer          { return r.upgradeRow }
func (r syncRow) done(summary string, _ bool) { r.finish(summary) }

// syncShowingProgress runs syncWorkspace with a progress bar when stderr is a
// terminal, and quietly otherwise.
func syncShowingProgress(r Remote, b Binding, root, direction string, force bool, options ...bool) error {
	board := newUpgradeBoard(os.Stderr)
	if !board.live {
		return syncWorkspace(r, b, root, direction, force, options...)
	}
	row := board.row(direction + " " + b.Remote)
	previous := syncProgress
	syncProgress = syncRow{row}
	defer func() { syncProgress = previous }()
	board.start()
	err := syncWorkspace(r, b, root, direction, force, options...)
	row.settle(err)
	board.close()
	return err
}

// transferExpectation guesses how long moving size bytes takes, so the bar
// creeps at a believable pace when the transport reports nothing.
func transferExpectation(size int64) time.Duration {
	const assumedRate = 4 << 20 // bytes per second
	return max(time.Second, time.Duration(float64(size)/assumedRate*float64(time.Second)))
}

func updateDataSize(files Snapshot) int64 {
	var total int64
	for _, f := range files {
		total += int64(len(f.Data) + len(f.Link))
	}
	return total
}

// progressReader reports the share of total bytes read so far.
type progressReader struct {
	r           io.Reader
	done, total int64
	report      func(float64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if p.total > 0 {
		p.report(float64(p.done) / float64(p.total))
	}
	return n, err
}
