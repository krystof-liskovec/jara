package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

type maintenanceLease struct {
	Token   string
	Expires time.Time
}
type maintenanceStatus struct {
	Busy   bool
	Reason string
}

func maintenancePath() string { return filepath.Join(fsutil.DataRoot(), ".maintenance.json") }
func activeMaintenance() (maintenanceLease, bool, error) {
	var lease maintenanceLease
	b, err := os.ReadFile(maintenancePath())
	if errors.Is(err, os.ErrNotExist) {
		return lease, false, nil
	}
	if err != nil {
		return lease, false, err
	}
	if err = json.Unmarshal(b, &lease); err != nil {
		return lease, false, err
	}
	return lease, time.Now().Before(lease.Expires), nil
}
func admitWorkspace() error {
	_, active, err := activeMaintenance()
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("environment is updating Codex; retry shortly")
	}
	return nil
}

func updateBusy() (maintenanceStatus, error) {
	// Count every session on Jara's socket, including sessions without metadata.
	b, err := tmux("list-sessions", "-F", "#{session_name}|#{@jara_background}|#{pane_dead}").Output()
	if err != nil {
		if _, e := sessions(); e != nil {
			return maintenanceStatus{}, e
		}
	} else {
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			fields := strings.Split(line, "|")
			if len(fields) == 3 && fields[1] == "1" && fields[2] == "1" {
				continue
			}
			if line != "" {
				return maintenanceStatus{true, "tmux sessions exist (including detached or idle sessions)"}, nil
			}
		}
	}
	locks, err := filepath.Glob(filepath.Join(fsutil.DataRoot(), "workspaces", "*.lock"))
	if err != nil {
		return maintenanceStatus{}, err
	}
	if len(locks) > 0 {
		return maintenanceStatus{true, "workspace operation in progress"}, nil
	}
	processes, err := os.ReadDir("/proc")
	if err != nil {
		return maintenanceStatus{}, err
	}
	sshd := 0
	for _, p := range processes {
		if _, e := strconv.Atoi(p.Name()); e != nil {
			continue
		}
		comm, e := os.ReadFile(filepath.Join("/proc", p.Name(), "comm"))
		if e != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name == "sshd" || name == "sshd-session" || name == "sshd-auth" {
			sshd++
		}
	}
	if sshd > 1 {
		return maintenanceStatus{true, "SSH connections are open"}, nil
	}
	return maintenanceStatus{}, nil
}

func maintenanceCommand(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("invalid maintenance invocation")
	}
	release, err := lockLifecycle()
	if err != nil {
		return err
	}
	defer release()
	lease, active, err := activeMaintenance()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if active {
			return json.NewEncoder(os.Stdout).Encode(maintenanceStatus{true, "another update is in progress"})
		}
		s, err := updateBusy()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "begin":
		if len(args) != 2 || !nameRE.MatchString(args[1]) {
			return fmt.Errorf("maintenance requires a valid lease token")
		}
		if active {
			return json.NewEncoder(os.Stdout).Encode(maintenanceStatus{true, "another update is in progress"})
		}
		s, err := updateBusy()
		if err != nil {
			return err
		}
		if s.Busy {
			return json.NewEncoder(os.Stdout).Encode(s)
		}
		if err = fsutil.WriteJSON(maintenancePath(), maintenanceLease{Token: args[1], Expires: time.Now().Add(10 * time.Minute)}); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "end":
		if len(args) != 2 {
			return fmt.Errorf("maintenance token required")
		}
		if lease.Token != "" && lease.Token != args[1] {
			return fmt.Errorf("maintenance lease belongs to another update")
		}
		err = os.Remove(maintenancePath())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	default:
		return fmt.Errorf("unknown maintenance action")
	}
}
