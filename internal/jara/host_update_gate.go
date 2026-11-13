package jara

import (
	"fmt"
	"io"
)

// Updates wait until nothing uses the environment. --force updates anyway and
// interrupts whatever the maintenance gate lists.

const forceUpdateHint = "Re-run with --force to update anyway; that interrupts everything listed above."

func printUpdateBlockers(log io.Writer, status maintenanceStatus) {
	for _, blocker := range status.Blockers {
		fmt.Fprintln(log, "  -", blocker)
	}
}

// admitUpdate reports whether an update may continue past the check made
// before building, explaining a deferral or what --force overrides.
func admitUpdate(log io.Writer, status maintenanceStatus, force bool) bool {
	if !status.Busy {
		return true
	}
	if !force {
		fmt.Fprintln(log, "Deferred:", status.Reason)
		printUpdateBlockers(log, status)
		fmt.Fprintln(log, forceUpdateHint)
		return false
	}
	fmt.Fprintln(log, "WARNING: updating anyway (--force):", status.Reason)
	printUpdateBlockers(log, status)
	return true
}

// beginUpdate takes the maintenance lease that blocks new workspace operations
// while the container is replaced. It reports false when the update is deferred.
func (u hostUpdater) beginUpdate(container, lease string, force bool, log io.Writer) (bool, error) {
	extra := []string{}
	if force {
		extra = append(extra, "force")
	}
	status, err := u.maintenance(container, "begin", lease, extra...)
	if err != nil {
		if !force {
			return false, err
		}
		// A server from before --force rejects the extra argument.
		fmt.Fprintln(log, "WARNING: the running Jara cannot take a forced update gate, so new workspace operations are not blocked during the replacement:", err)
		return true, nil
	}
	if status.Busy && !status.Forced {
		fmt.Fprintln(log, "Deferred after build:", status.Reason)
		printUpdateBlockers(log, status)
		if !force {
			fmt.Fprintln(log, forceUpdateHint)
		}
		return false, nil
	}
	if status.Forced && len(status.Blockers) > 0 {
		fmt.Fprintln(log, "WARNING: replacing the container now interrupts:")
		printUpdateBlockers(log, status)
	}
	return true, nil
}
