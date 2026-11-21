package jara

import (
	"bufio"
	"os/exec"
	"strings"
	"time"
)

// sleepBudget is how long a device may delay suspending to save its work;
// logind allows delay locks five seconds by default (InhibitDelayMaxSec).
const sleepBudget = 3 * time.Second

const (
	logindSleepSignal    = "PrepareForSleep"
	logindShutdownSignal = "PrepareForShutdown"
)

// watchSleep calls before when the system is about to suspend or shut down
// (shutdown true), with a short delay granted to finish. It uses
// systemd-logind's delay inhibitors and its PrepareForSleep and
// PrepareForShutdown signals through systemd-inhibit and dbus-monitor;
// without them (macOS, non-systemd Linux) it returns at once and the host
// notices the silent device instead. When logind refuses a shutdown delay
// lock (a polkit policy), it keeps watching for suspend alone.
func watchSleep(stop <-chan struct{}, before func(shutdown bool)) {
	inhibit, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return
	}
	monitor, err := exec.LookPath("dbus-monitor")
	if err != nil {
		return
	}
	what := "sleep:shutdown"
	for {
		args := []string{"--what=" + what, "--mode=delay", "--who=Jara",
			"--why=Hand the running agent to the remote host", monitor, "--system", logindMatch(logindSleepSignal)}
		if what != "sleep" {
			args = append(args, logindMatch(logindShutdownSignal))
		}
		cmd := exec.Command(inhibit, args...)
		stdout, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return
		}
		started := time.Now()
		events := make(chan bool, 1) // true: shutdown
		go scanLogindSignals(bufio.NewScanner(stdout), events)
		var shutdown, ok bool
		select {
		case <-stop:
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return
		case shutdown, ok = <-events:
			if ok {
				before(shutdown)
			}
		}
		// Ending the inhibitor releases the delay lock, letting the system
		// sleep or power off; the watch starts again after it wakes.
		_ = cmd.Process.Kill()
		waitErr := cmd.Wait()
		if !ok && what != "sleep" && waitErr != nil && time.Since(started) < 2*time.Second {
			what = "sleep" // the shutdown lock was refused: keep the sleep one
			continue
		}
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func logindMatch(member string) string {
	return "type='signal',interface='org.freedesktop.login1.Manager',member='" + member + "'"
}

// scanLogindSignals sends one event when logind announces a suspend
// (false) or shutdown (true), and closes events when the monitor ends.
func scanLogindSignals(scanner *bufio.Scanner, events chan<- bool) {
	member := ""
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, "member="+logindSleepSignal):
			member = logindSleepSignal
		case strings.Contains(line, "member="+logindShutdownSignal):
			member = logindShutdownSignal
		case member != "" && strings.Contains(line, "boolean true"):
			events <- member == logindShutdownSignal
			return
		case member != "" && strings.Contains(line, "boolean false"):
			member = "" // resuming, or a cancelled shutdown
		}
	}
	close(events)
}
