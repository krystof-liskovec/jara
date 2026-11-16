package jara

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// runnerCommand is `jara runner status [WORKSPACE_ID] [--remote NAME]`: which
// machine runs a workspace's agent and when its last checkpoint was saved.
func runnerCommand(c Config, args []string) error {
	if len(args) == 0 || args[0] != "status" {
		return fmt.Errorf("use jara runner status [WORKSPACE_ID] [--remote NAME]")
	}
	remoteName, positional, err := workspaceCommandArgs(args[1:], "")
	if err != nil {
		return err
	}
	if len(positional) > 1 {
		return fmt.Errorf("use jara runner status [WORKSPACE_ID] [--remote NAME]")
	}
	workspace := ""
	if len(positional) == 1 {
		workspace = positional[0]
	} else if binding, ok := currentFolderBinding(c); ok {
		workspace = binding.Workspace
		if remoteName == "" {
			remoteName = binding.Remote
		}
	} else {
		return fmt.Errorf("this folder has no workspace; pass a workspace ID")
	}
	if remoteName == "" {
		remoteName = c.Default
	}
	remoteName, remote, err := resolveConfigRemote(c, remoteName)
	if err != nil {
		return err
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
	if lease.Want != "" {
		fmt.Printf("%s is waiting to take it back when the current turn ends.\n", lease.Want)
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
