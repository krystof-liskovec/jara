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

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/fsutil"
)

type maintenanceLease struct {
	Token   string
	Expires time.Time
}
type maintenanceStatus struct {
	Busy     bool
	Reason   string
	Blockers []string `json:",omitempty"` // each session, operation or connection that defers the update
	Forced   bool     `json:",omitempty"` // begin took the lease despite the blockers
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

func sshdProcess(pid int) bool {
	if pid < 1 {
		return false
	}
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return false
	}
	name := strings.TrimSpace(string(comm))
	return name == "sshd" || name == "sshd-session" || name == "sshd-auth"
}

// maintenanceCommand runs `jara _maintenance status|begin LEASE [force]|end LEASE`.
// A forced begin stops idle Jara sessions as usual, then takes the lease even
// though blockers remain; they are reported so the caller can warn about them.
func maintenanceCommand(args []string) error {
	if len(args) < 1 || len(args) > 3 {
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
	activeLease := func() maintenanceStatus {
		return maintenanceStatus{Busy: true, Reason: "another update is in progress",
			Blockers: []string{"another update holds the maintenance lease until " + lease.Expires.Local().Format("15:04:05")}}
	}
	switch args[0] {
	case "status":
		if active {
			return json.NewEncoder(os.Stdout).Encode(activeLease())
		}
		s, err := updateBusy()
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	case "begin":
		force := len(args) == 3 && args[2] == "force"
		if len(args) < 2 || !nameRE.MatchString(args[1]) || len(args) == 3 && !force {
			return fmt.Errorf("maintenance requires a valid lease token")
		}
		if active && !force {
			return json.NewEncoder(os.Stdout).Encode(activeLease())
		}
		s, err := updateBusy()
		if err != nil {
			return err
		}
		if s.Busy && !force {
			return json.NewEncoder(os.Stdout).Encode(s)
		}
		stopped, err := stopIdleChatDaemonsForUpdate()
		if err != nil {
			return err
		}
		if stopped.Busy && !force {
			return json.NewEncoder(os.Stdout).Encode(stopped)
		}
		if force {
			// Report what the forced update interrupts.
			if s, err = updateBusy(); err != nil {
				return err
			}
			if active {
				s.Busy, s.Blockers = true, append(activeLease().Blockers, s.Blockers...)
			}
			s.Blockers = append(s.Blockers, stopped.Blockers...)
			s.Forced = true
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
