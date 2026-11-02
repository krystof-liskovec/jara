package jara

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const defaultSessionIdleTimeout = 24 * time.Hour
const sessionJanitorInterval = 10 * time.Minute

type managedSession struct {
	name          string
	attached      int
	workspace     string
	lastAttached  int64
	hookInstalled bool
	background    bool
	paneDead      bool
}

func parseSessionIdleTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultSessionIdleTimeout, nil
	}
	if raw == "0" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout < time.Minute {
		return 0, fmt.Errorf("must be 0 or a duration of at least 1m, such as 24h")
	}
	return timeout, nil
}

func configuredSessionIdleTimeout() (time.Duration, error) {
	return parseSessionIdleTimeout(os.Getenv("JARA_SESSION_IDLE_TIMEOUT"))
}

func validateSessionSettings() error {
	if _, err := maxSessions(); err != nil {
		return err
	}
	_, err := configuredSessionIdleTimeout()
	return err
}

func sessionJanitorCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("invalid session janitor invocation")
	}
	timeout, err := configuredSessionIdleTimeout()
	if err != nil {
		return err
	}
	if timeout == 0 {
		return nil
	}
	for {
		if err := expireDetachedSessions(time.Now(), timeout); err != nil {
			fmt.Fprintf(os.Stderr, "jara session cleanup: %v\n", err)
		}
		time.Sleep(sessionJanitorInterval)
	}
}

func initializeSessionLifecycle(session string) error {
	if err := setLastAttached(session, time.Now()); err != nil {
		return err
	}
	return installSessionAttachHook(session)
}

func markSessionAttached(session string) error {
	unlock, err := lockLifecycle()
	if err != nil {
		return err
	}
	defer unlock()
	if err = installSessionAttachHook(session); err != nil {
		return err
	}
	return setLastAttached(session, time.Now())
}

func recordSessionAttachment(session string) error {
	if !nameRE.MatchString(session) {
		return fmt.Errorf("invalid session name")
	}
	return setLastAttached(session, time.Now())
}

func setLastAttached(session string, at time.Time) error {
	return runTmux("set-option", "-t", session, "@jara_last_attached", strconv.FormatInt(at.UnixNano(), 10))
}

func installSessionAttachHook(session string) error {
	installed, err := tmux("show-option", "-v", "-t", session, "@jara_idle_hook").Output()
	if err == nil && strings.TrimSpace(string(installed)) == "1" {
		return nil
	}
	command := "run-shell -b '/usr/local/bin/jara _session-attached " + session + "'"
	if err = runTmux("set-hook", "-t", session, "client-attached", command); err != nil {
		return fmt.Errorf("configure session attachment tracking: %w", err)
	}
	return runTmux("set-option", "-t", session, "@jara_idle_hook", "1")
}

func expireDetachedSessions(now time.Time, timeout time.Duration) error {
	unlock, err := lockLifecycle()
	if err != nil {
		return err
	}
	defer unlock()
	sessions, err := listManagedSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		if !session.hookInstalled {
			if err := installSessionAttachHook(session.name); err != nil {
				continue
			}
			if session.lastAttached == 0 {
				_ = setLastAttached(session.name, now)
			}
			continue
		}
		if session.lastAttached == 0 {
			_ = setLastAttached(session.name, now)
			continue
		}
		if session.background && !session.paneDead {
			continue
		}
		if session.attached != 0 || now.Sub(time.Unix(0, session.lastAttached)) < timeout {
			continue
		}
		current, err := tmux("display-message", "-p", "-t", session.name, "#{session_attached}\t#{@jara_last_attached}").Output()
		if err != nil {
			continue
		}
		fields := strings.Split(strings.TrimSpace(string(current)), "\t")
		if len(fields) != 2 || fields[0] != "0" || fields[1] != strconv.FormatInt(session.lastAttached, 10) {
			continue
		}
		if err := tmux("kill-session", "-t", session.name).Run(); err != nil {
			continue
		}
		fmt.Fprintf(os.Stderr, "jara: closed detached session %s after %s since last attachment\n", session.name, timeout)
	}
	return nil
}

func listManagedSessions() ([]managedSession, error) {
	output, err := tmux("list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{@jara_workspace}\t#{@jara_last_attached}\t#{@jara_idle_hook}\t#{@jara_background}\t#{pane_dead}").Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && (strings.Contains(string(exitErr.Stderr), "no server running") || strings.Contains(string(exitErr.Stderr), "No such file")) {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions for cleanup: %w", err)
	}
	var sessions []managedSession
	for _, line := range strings.Split(strings.TrimRight(string(output), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 7 || fields[2] == "" {
			continue
		}
		attached, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		lastAttached, _ := strconv.ParseInt(fields[3], 10, 64)
		sessions = append(sessions, managedSession{
			name:          fields[0],
			attached:      attached,
			workspace:     fields[2],
			lastAttached:  lastAttached,
			hookInstalled: fields[4] == "1",
			background:    fields[5] == "1",
			paneDead:      fields[6] == "1",
		})
	}
	return sessions, nil
}
