package jara

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const runnerUsage = "use jara runner status [WORKSPACE_ID] [--remote NAME] or jara runner prefer [DEVICE...|--none] [--workspace ID] [--remote NAME]"

// runnerCommand is `jara runner status [WORKSPACE_ID] [--remote NAME]`: which
// machine runs a workspace's agent and when its last checkpoint was saved,
// and `jara runner prefer DEVICE...`: which device gets the conversation back
// first when several have a window waiting for the remote host.
func runnerCommand(c Config, args []string) error {
	if len(args) == 0 || args[0] != "status" && args[0] != "prefer" {
		return errors.New(runnerUsage)
	}
	action := args[0]
	workspace, devices, clear, rest := "", []string(nil), false, args[1:]
	if action == "prefer" {
		rest = nil
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--none":
				clear = true
			case args[i] == "--workspace" && i+1 < len(args):
				i++
				workspace = args[i]
			case args[i] == "--remote" && i+1 < len(args):
				rest = append(rest, args[i], args[i+1])
				i++
			case strings.HasPrefix(args[i], "-"):
				return errors.New(runnerUsage)
			default:
				if !deviceNameRE.MatchString(args[i]) && args[i] != "" {
					devices = append(devices, args[i])
				} else {
					return fmt.Errorf("invalid device name %q", args[i])
				}
			}
		}
		if clear && len(devices) > 0 {
			return errors.New(runnerUsage)
		}
	}
	remoteName, positional, err := workspaceCommandArgs(rest, "")
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return errors.New(runnerUsage)
	}
	if len(positional) == 1 {
		workspace = positional[0]
	}
	if workspace == "" {
		binding, ok := currentFolderBinding(c)
		if !ok {
			return fmt.Errorf("this folder has no workspace; pass a workspace ID")
		}
		workspace = binding.Workspace
		if remoteName == "" {
			remoteName = binding.Remote
		}
	}
	if remoteName == "" {
		remoteName = c.Default
	}
	remoteName, remote, err := resolveConfigRemote(c, remoteName)
	if err != nil {
		return err
	}
	if action == "prefer" && (clear || len(devices) > 0) {
		if err = rpc(remote, Request{Op: "runner-prefer", ID: workspace, Devices: devices}, nil, nil); err != nil {
			return err
		}
	}
	var status runnerStatus
	if err = rpc(remote, Request{Op: "runner-status", ID: workspace}, nil, &status); err != nil {
		return err
	}
	lease := status.Lease
	switch {
	case lease.Owner == "":
		fmt.Printf("No device holds %s; %s runs its agent when you connect there.\n", workspace, remoteName)
	case status.Online:
		fmt.Printf("%s runs the agent of %s and is online; other devices attach to its live session through %s.\n", lease.Owner, workspace, remoteName)
	default:
		fmt.Printf("%s runs the agent of %s but is offline; the remote host continues its work, or the next device that opens it.\n", lease.Owner, workspace)
	}
	if lease.Seq > 0 {
		fmt.Printf("Last checkpoint: %s by %s (conversation %s).\n", time.Unix(lease.CheckpointAt, 0).Format(time.DateTime), lease.Writer, lease.Conversation)
	}
	if len(lease.Wants) > 0 {
		var waiting []string
		for device := range lease.Wants {
			waiting = append(waiting, device)
		}
		sort.Slice(waiting, func(i, j int) bool { return lease.preferenceRank(waiting[i]) < lease.preferenceRank(waiting[j]) })
		fmt.Printf("Waiting to take it back when the remote host is idle: %s.\n", strings.Join(waiting, ", "))
	}
	if len(lease.Prefers) > 0 {
		fmt.Printf("Preferred devices: %s.\n", strings.Join(lease.Prefers, ", "))
	}
	if lease.LastRunner != "" {
		fmt.Printf("Last runner: %s.\n", lease.LastRunner)
	}
	return nil
}

// currentFolderBinding finds the workspace associated with this folder or
// its Git repository root.
func currentFolderBinding(c Config) (Binding, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return Binding{}, false
	}
	if resolved, e := filepath.EvalSymlinks(cwd); e == nil {
		cwd = resolved
	}
	for _, dir := range []string{cwd, gitTopLevel(cwd)} {
		if binding, ok := c.Bindings[dir]; ok && dir != "" {
			return binding, true
		}
	}
	return Binding{}, false
}

func gitTopLevel(dir string) string {
	top, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	if resolved, e := filepath.EvalSymlinks(top); e == nil {
		return resolved
	}
	return top
}
