//go:build linux || darwin

package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// daemonStopTimeout is how long the daemon gives its sessions to hand their
// work over when it stops; the service manager's own timeout is longer.
const daemonStopTimeout = 25 * time.Second

// daemonSpawnable lists the session commands the daemon starts.
var daemonSpawnable = map[string]bool{"_runner-daemon": true}

// daemonSupervisor is `jara daemon run`: one per user, normally started by
// the service manager. It starts this device's agent sessions as its own
// children, continues the ones a shutdown interrupted, and stops them
// gracefully when the system powers off.
type daemonSupervisor struct {
	mu       sync.Mutex
	children map[int]*exec.Cmd
	exited   chan struct{}
}

func runDaemonSupervisor() error {
	path := daemonSocketPath()
	if len(path) >= 100 {
		return fmt.Errorf("Jara data path is too long for the daemon socket: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := callDaemon(daemonMessage{Op: "ping"}); err == nil {
		return fmt.Errorf("the Jara daemon is already running")
	}
	_ = os.Remove(path)
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	if err = os.Chmod(path, 0600); err != nil {
		listener.Close()
		return err
	}
	s := &daemonSupervisor{children: map[int]*exec.Cmd{}, exited: make(chan struct{}, 1)}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(signals)
	fmt.Fprintf(os.Stderr, "Jara daemon %s started (pid %d).\n", Version, os.Getpid())
	// Sessions it starts for other devices run with your shell's environment.
	adoptDeviceEnvironment()
	s.resumeInterrupted()
	stop := make(chan struct{})
	go func() {
		if serveErr := serveDeviceRemote(stop); serveErr != nil {
			fmt.Fprintln(os.Stderr, "Device requests are unavailable:", serveErr)
		}
	}()
	go runDeviceLinks(stop)
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	received := <-signals
	close(stop)
	listener.Close()
	fmt.Fprintf(os.Stderr, "Jara daemon stopping (%v); handing running sessions over.\n", received)
	s.stopAll(daemonStopTimeout)
	return nil
}

func (s *daemonSupervisor) serve(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReaderSize(conn, 64*1024)
	line, err := reader.ReadBytes('\n')
	var request daemonMessage
	if err != nil || json.Unmarshal(line, &request) != nil {
		return
	}
	answer := daemonMessage{Version: Version, PID: os.Getpid()}
	switch request.Op {
	case "ping":
		s.mu.Lock()
		answer.Sessions = len(s.children)
		s.mu.Unlock()
	case "spawn":
		if request.Request == nil {
			answer.Error = "spawn needs a request"
			break
		}
		pid, spawnErr := s.spawn(request.Executable, request.Command, *request.Request, request.Log, request.Env)
		if spawnErr != nil {
			answer.Error = spawnErr.Error()
		}
		answer.PID = pid
	default:
		answer.Error = "unknown daemon operation " + strconv.Quote(request.Op)
	}
	_ = json.NewEncoder(conn).Encode(answer)
}

// spawn starts `EXECUTABLE COMMAND REQUEST` in its own session with the
// requesting client's environment, logging to logPath.
func (s *daemonSupervisor) spawn(executable, command string, request Request, logPath string, env []string) (int, error) {
	if !daemonSpawnable[command] {
		return 0, fmt.Errorf("the Jara daemon does not start %q", command)
	}
	if !nameRE.MatchString(request.ID) {
		return 0, fmt.Errorf("invalid workspace ID")
	}
	if !filepath.IsAbs(executable) || !filepath.IsAbs(logPath) {
		return 0, fmt.Errorf("the executable and log need absolute paths")
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		return 0, fmt.Errorf("cannot run %s", executable)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	input, err := os.Open(os.DevNull)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	cmd := exec.Command(executable, command, token(request))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, log, log
	if len(env) > 0 {
		cmd.Env = env
	}
	if info, statErr := os.Stat(request.Root); statErr == nil && info.IsDir() {
		cmd.Dir = request.Root
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return 0, fmt.Errorf("start session: %w", err)
	}
	pid := cmd.Process.Pid
	s.mu.Lock()
	s.children[pid] = cmd
	s.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		s.mu.Lock()
		delete(s.children, pid)
		s.mu.Unlock()
		select {
		case s.exited <- struct{}{}:
		default:
		}
	}()
	return pid, nil
}

// resumeInterrupted continues the sessions of local-only folders that were
// working when the device last shut down.
func (s *daemonSupervisor) resumeInterrupted() {
	for _, id := range pendingDaemonResumes() {
		if chatDaemonRunning(id) {
			continue // a window already reopened it
		}
		entry, ok := takeDaemonResume(id)
		if !ok {
			continue
		}
		paths, err := chatDaemonPathSet(id)
		if err != nil {
			continue
		}
		pid, err := s.spawn(entry.Executable, "_runner-daemon", entry.Request, paths.log, entry.Env)
		if err == nil {
			err = os.WriteFile(paths.pid, []byte(strconv.Itoa(pid)+"\n"), 0600)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not continue the interrupted session in %s: %v\n", entry.Request.Root, err)
			continue
		}
		fmt.Fprintf(os.Stderr, "Continuing the interrupted session in %s.\n", entry.Request.Root)
	}
}

// stopAll asks every session to stop, which hands running work to a remote
// host or records it to continue, and waits for them up to timeout.
func (s *daemonSupervisor) stopAll(timeout time.Duration) {
	s.mu.Lock()
	for pid := range s.children {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	s.mu.Unlock()
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		remaining := len(s.children)
		s.mu.Unlock()
		if remaining == 0 {
			return
		}
		select {
		case <-s.exited:
		case <-time.After(time.Second):
		case <-deadline:
			fmt.Fprintf(os.Stderr, "%d session(s) did not stop in time.\n", remaining)
			return
		}
	}
}
