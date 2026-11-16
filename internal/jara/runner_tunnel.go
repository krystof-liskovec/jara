package jara

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// localRunner keeps the remote host's copy of a workspace current while this
// device runs its agent, renews the device's lease over a link that also
// carries the tunnel to the live session, and hands the workspace over.
type localRunner struct {
	remote   Remote
	binding  Binding
	request  Request
	paths    chatDaemonPaths        // this session's queue, schedules and crunch task
	status   func() runnerHeartbeat // what the session is doing
	released bool

	checkpointMu sync.Mutex // one checkpoint at a time
	pendingMu    sync.Mutex
	pending      *chatDaemonState // newest state waiting for a checkpoint
	running      bool

	linkMu  sync.Mutex
	link    io.Writer // heartbeats to the current link, if connected
	linkErr error

	lostOnce sync.Once
	lost     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

func newLocalRunner(remote Remote, q Request, paths chatDaemonPaths, status func() runnerHeartbeat) *localRunner {
	return &localRunner{remote: remote, binding: Binding{Remote: q.RemoteName, Workspace: q.Workspace}, request: q,
		paths: paths, status: status, lost: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{})}
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

// markLost stops this session: another machine continues the conversation.
func (lr *localRunner) markLost() {
	lr.lostOnce.Do(func() {
		close(lr.lost)
		_ = updateLocalRunnerMarker(lr.request.ID, func(m *localRunnerMarker) { m.Lost = true })
	})
}

func (lr *localRunner) wire(op string) Request {
	return Request{Op: op, ID: lr.binding.Workspace, Runner: lr.request.Runner, Epoch: lr.request.Epoch}
}

// checkpointLater saves the session in the background. Requests made while a
// checkpoint runs are coalesced into one more checkpoint.
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
// host, then stores the conversations that changed and pending schedules.
func (lr *localRunner) checkpoint(state chatDaemonState) error {
	lr.checkpointMu.Lock()
	defer lr.checkpointMu.Unlock()
	if lr.isLost() {
		return errRunnerLost
	}
	if err := syncWorkspace(lr.remote, lr.binding, lr.request.Root, "push", true, false, true); err != nil {
		return fmt.Errorf("copy files to the remote: %w", err)
	}
	marker := loadLocalRunnerMarker(lr.request.ID)
	checkpoint, versions, err := collectCheckpoint(lr.request.Root, lr.request.Runner, state.Conversation, marker.Versions)
	if err != nil {
		return err
	}
	checkpoint.Schedules = pendingSchedules(lr.paths.schedules)
	checkpoint.Crunch = activeCrunch(lr.paths.crunch)
	data, err := json.Marshal(checkpoint)
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
	return updateLocalRunnerMarker(lr.request.ID, func(m *localRunnerMarker) { m.Seq, m.Versions = answer.Seq, versions })
}

// release answers a jara/release request once this session is idle.
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
	fmt.Fprintln(os.Stderr, "Handed the workspace over.")
	return nil
}

// goingToSleep runs just before the device suspends. Idle sessions stay
// here; a running turn or scheduled work is checkpointed as far as the short
// sleep delay allows, and the host continues it.
func (lr *localRunner) goingToSleep(state chatDaemonState, budget time.Duration) {
	beat := lr.status()
	if !beat.Busy && !beat.Pending {
		return
	}
	done := make(chan struct{})
	go func() { _ = lr.checkpoint(state); close(done) }()
	select {
	case <-done:
	case <-time.After(budget):
	}
	beat.Sleep = true
	lr.sendBeat(beat)
	lr.markLost()
}

func (lr *localRunner) sendBeat(beat runnerHeartbeat) {
	line, _ := json.Marshal(beat)
	lr.linkMu.Lock()
	defer lr.linkMu.Unlock()
	if lr.link != nil {
		_, lr.linkErr = lr.link.Write(append(line, '\n'))
	}
}

func (lr *localRunner) stopLink() {
	lr.stopOnce.Do(func() { close(lr.stop) })
	select {
	case <-lr.done:
	case <-time.After(5 * time.Second):
	}
}

// linkLoop keeps one SSH connection to the remote host: it forwards a socket
// there to this device's session and runs `jara _runner-link`, which renews
// the lease with each heartbeat. It reconnects after network changes; a
// refused link means another machine took over.
func (lr *localRunner) linkLoop(local string) {
	defer close(lr.done)
	backoff := time.Second
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
			err = lr.runLink(answer.Socket, local)
			if lr.isLost() {
				return
			}
			if time.Since(started) > time.Minute {
				backoff = time.Second
			}
		}
		fmt.Fprintf(os.Stderr, "Link to the remote host closed (%v); retrying in %s.\n", err, backoff)
		select {
		case <-lr.stop:
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (lr *localRunner) runLink(socket, local string) error {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	wire := lr.wire("")
	wire.Link = hex.EncodeToString(nonce)
	args := append(sshArgs(lr.remote, false), "-o", "ExitOnForwardFailure=yes", "-R", socket+":"+local,
		"--", lr.remote.Host, shellArgs("jara", "_runner-link", token(wire)))
	cmd := exec.Command("ssh", args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err = cmd.Start(); err != nil {
		return err
	}
	lr.linkMu.Lock()
	lr.link, lr.linkErr = stdin, nil
	lr.linkMu.Unlock()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	ticker := time.NewTicker(runnerHeartbeatInterval())
	defer ticker.Stop()
	lr.sendBeat(lr.status())
	for {
		select {
		case <-ticker.C:
			lr.sendBeat(lr.status())
		case <-lr.stop:
			stdin.Close()
			_ = cmd.Process.Kill()
			<-exited
			return nil
		case err = <-exited:
			lr.linkMu.Lock()
			lr.link = nil
			lr.linkMu.Unlock()
			if strings.Contains(stderr.String(), runnerLostMessage) {
				lr.markLost()
			}
			return fmt.Errorf("%v %s", err, strings.TrimSpace(stderr.String()))
		}
	}
}

// midTurnCheckpoints saves long turns periodically, so a device that
// disappears without warning loses little work. JARA_RUNNER_CHECKPOINT
// (seconds) changes the interval.
func (lr *localRunner) midTurnCheckpoints(state func() chatDaemonState) {
	interval := 20 * time.Second
	if seconds, err := strconv.Atoi(os.Getenv("JARA_RUNNER_CHECKPOINT")); err == nil && seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-lr.stop:
			return
		case <-lr.lost:
			return
		case <-ticker.C:
			if current := state(); current.ActiveTurn {
				lr.checkpointLater(current)
			}
		}
	}
}
