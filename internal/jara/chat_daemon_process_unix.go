//go:build linux || darwin

package jara

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func startChatDaemonProcess(request Request, logPath string) (int, error) {
	if request.Runner == "" {
		return startDetachedJara("_chat-daemon", request, logPath)
	}
	// The agent runs on this device: under the Jara daemon when it runs, so
	// the session outlives this terminal and logging out.
	if pid, err := spawnViaDaemon("_runner-daemon", request, logPath); err == nil {
		return pid, nil
	}
	return startDetachedJara("_runner-daemon", request, logPath)
}

// startDetachedJara runs `jara COMMAND REQUEST` in its own session, so it
// outlives the SSH connection or terminal that started it.
func startDetachedJara(command string, request Request, logPath string) (int, error) {
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
	executable, err := os.Executable()
	if err != nil {
		return 0, err
	}
	cmd := exec.Command(executable, command, token(request))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = input, log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return 0, fmt.Errorf("start background Jara process: %w", err)
	}
	// Reap the daemon if it exits while this process still runs; an unreaped
	// child stays a zombie that still looks alive, hiding a startup crash.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

func chatProcessExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func chatDaemonTerminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}
