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

// watchSleep calls before when the system is about to suspend, with a short
// delay granted to finish. It uses systemd-logind's delay inhibitor and
// PrepareForSleep signal through systemd-inhibit and dbus-monitor; without
// them (macOS, non-systemd Linux) it returns at once and the host notices
// the silent device instead.
func watchSleep(stop <-chan struct{}, before func()) {
	inhibit, err := exec.LookPath("systemd-inhibit")
	if err != nil {
		return
	}
	monitor, err := exec.LookPath("dbus-monitor")
	if err != nil {
		return
	}
	for {
		cmd := exec.Command(inhibit, "--what=sleep", "--mode=delay", "--who=Jara",
			"--why=Hand the running agent to the remote host", monitor, "--system",
			"type='signal',interface='org.freedesktop.login1.Manager',member='PrepareForSleep'")
		stdout, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return
		}
		sleeping := make(chan struct{}, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			signal := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "member=PrepareForSleep") {
					signal = true
				} else if signal && strings.Contains(line, "boolean true") {
					sleeping <- struct{}{}
					return
				}
			}
			close(sleeping)
		}()
		select {
		case <-stop:
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return
		case _, ok := <-sleeping:
			if ok {
				before()
			}
		}
		// Ending the inhibitor releases the delay lock, letting the system
		// sleep; the watch starts again after it wakes.
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		select {
		case <-stop:
			return
		case <-time.After(5 * time.Second):
		}
	}
}
