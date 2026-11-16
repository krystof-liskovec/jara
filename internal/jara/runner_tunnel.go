package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// localRunner keeps the remote host's copy of a workspace current while this
// device runs its agent, and holds the tunnel through which other devices
// reach the live session.
type localRunner struct {
	remote   Remote
	binding  Binding
	request  Request
	released bool

	checkpointMu sync.Mutex // one checkpoint at a time
	pendingMu    sync.Mutex
	pending      *chatDaemonState // newest state waiting for a checkpoint
	running      bool

	lostOnce sync.Once
	lost     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	tunnel   chan struct{}
}

func newLocalRunner(remote Remote, q Request) *localRunner {
	return &localRunner{remote: remote, binding: Binding{Remote: q.RemoteName, Workspace: q.Workspace}, request: q,
		lost: make(chan struct{}), stop: make(chan struct{}), tunnel: make(chan struct{})}
}

func (lr *localRunner) hooks() chatDaemonRunnerHooks {
	return chatDaemonRunnerHooks{turnEnded: lr.checkpointLater, release: lr.release, lost: lr.lost}
}

func (lr *localRunner) isLost() bool {
	select {
	case <-lr.lost:
		return true
	default:
		return false
	}
}

func (lr *localRunner) markLost() {
	lr.lostOnce.Do(func() {
		close(lr.lost)
		_ = updateLocalRunnerMarker(lr.request.ID, func(m *localRunnerMarker) { m.Lost = true })
	})
}

func (lr *localRunner) wire(op string) Request {
	return Request{Op: op, ID: lr.binding.Workspace, Runner: lr.request.Runner, Epoch: lr.request.Epoch}
}

// checkpointLater saves a finished turn in the background. Turns that end
// while a checkpoint runs are coalesced into one more checkpoint.
func (lr *localRunner) checkpointLater(state chatDaemonState) {
	lr.pendingMu.Lock()
	lr.pending = &state
	if lr.running {
		lr.pendingMu.Unlock()
		return
	}
	lr.running = true
	lr.pendingMu.Unlock()
	go func() {
		for {
			lr.pendingMu.Lock()
			next := lr.pending
			lr.pending = nil
			if next == nil {
				lr.running = false
				lr.pendingMu.Unlock()
				return
			}
			lr.pendingMu.Unlock()
			if err := lr.checkpoint(*next); err != nil {
				fmt.Fprintln(os.Stderr, "Checkpoint failed:", err)
			}
		}
	}()
}

// checkpoint mirrors the checkout (commits, edits and deletions) to the remote
// host, then stores the conversation there.
func (lr *localRunner) checkpoint(state chatDaemonState) error {
	lr.checkpointMu.Lock()
	defer lr.checkpointMu.Unlock()
	if lr.isLost() {
		return errRunnerLost
	}
	if err := syncWorkspace(lr.remote, lr.binding, lr.request.Root, "push", true, false, true); err != nil {
		return fmt.Errorf("copy files to the remote: %w", err)
	}
	if state.Conversation == "" {
		return nil
	}
	bundle, err := exportConversation(state.Conversation, lr.request.Root, lr.request.Runner)
	if err != nil {
		return err
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return err
	}
	var answer struct {
		Seq int64 `json:"seq"`
	}
	if err = rpc(lr.remote, lr.wire("runner-checkpoint"), bytes.NewReader(data), &answer); err != nil {
		if strings.Contains(err.Error(), runnerLostMessage) {
			lr.markLost()
		}
		return err
	}
	return updateLocalRunnerMarker(lr.request.ID, func(m *localRunnerMarker) { m.Seq = answer.Seq })
}

// release answers a jara/release request from another device's claim.
func (lr *localRunner) release(state chatDaemonState) bool {
	if err := lr.handOver(state); err != nil {
		fmt.Fprintln(os.Stderr, "Could not hand the workspace over:", err)
		return false
	}
	return true
}

// handOver saves a final checkpoint and frees the workspace's lease.
func (lr *localRunner) handOver(state chatDaemonState) error {
	if lr.isLost() {
		return errRunnerLost
	}
	if err := lr.checkpoint(state); err != nil {
		return err
	}
	if err := rpc(lr.remote, lr.wire("runner-release"), nil, nil); err != nil {
		if strings.Contains(err.Error(), runnerLostMessage) {
			lr.markLost()
		}
		return err
	}
	lr.released = true
	fmt.Fprintln(os.Stderr, "Handed the workspace over to the remote host.")
	return nil
}

func (lr *localRunner) stopTunnel() {
	lr.stopOnce.Do(func() { close(lr.stop) })
	select {
	case <-lr.tunnel:
	case <-time.After(5 * time.Second):
	}
}

// tunnelLoop forwards a socket on the remote host to this device's session,
// reconnecting after network changes. A refused tunnel means another device
// took over, which stops this session.
func (lr *localRunner) tunnelLoop(local string) {
	defer close(lr.tunnel)
	backoff := 2 * time.Second
	for {
		var answer struct {
			Socket string `json:"socket"`
		}
		err := rpc(lr.remote, lr.wire("runner-tunnel"), nil, &answer)
		if err != nil && strings.Contains(err.Error(), runnerLostMessage) {
			lr.markLost()
			return
		}
		if err == nil {
			started := time.Now()
			args := append(sshArgs(lr.remote, false), "-N", "-o", "ExitOnForwardFailure=yes",
				"-R", answer.Socket+":"+local, "--", lr.remote.Host)
			cmd := exec.Command("ssh", args...)
			cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
			if err = cmd.Start(); err == nil {
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case <-lr.stop:
					_ = cmd.Process.Kill()
					<-done
					return
				case err = <-done:
				}
				if time.Since(started) > time.Minute {
					backoff = 2 * time.Second
				}
			}
		}
		fmt.Fprintf(os.Stderr, "Tunnel to the remote host closed (%v); retrying in %s.\n", err, backoff)
		select {
		case <-lr.stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}
