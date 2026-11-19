package jara

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
)

// interruptHold keeps Ctrl+C, SIGTERM and SIGHUP from killing this process
// during a step that must finish or roll back. Commands already running still
// receive the terminal's signal and fail, which sends the step down its
// normal rollback path instead of abandoning it half done.
type interruptHold struct {
	signals chan os.Signal
	done    chan struct{}
	once    sync.Once
	got     atomic.Bool
}

func holdInterrupts(log io.Writer, note string) *interruptHold {
	h := &interruptHold{signals: make(chan os.Signal, 1), done: make(chan struct{})}
	signal.Notify(h.signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-h.signals:
				if !h.got.Swap(true) {
					fmt.Fprintln(log, note)
				}
			case <-h.done:
				return
			}
		}
	}()
	return h
}

// interrupted reports whether a signal arrived while the hold was active.
func (h *interruptHold) interrupted() bool { return h.got.Load() }

// release restores default signal handling; it is safe to call repeatedly.
func (h *interruptHold) release() {
	h.once.Do(func() {
		signal.Stop(h.signals)
		close(h.done)
	})
}
