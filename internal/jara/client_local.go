package jara

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"

	"krystof.liskovec/jara/internal/protocol"
)

// localSessionActions run without a remote; the rest need one.
var localSessionActions = map[string]bool{"open": true, "new": true, "resume": true, "sessions": true, "run": true, "handoff": true}

// localOpen is a session request for a folder no remote is attached to.
type localOpen struct {
	action, chatInterface, agent, note, prompt string
	root, rel                                  string
	codexArgs                                  []string
	latest, resumeLatest, worktree             bool
}

// workspaceUsesRemote reports whether a session in root goes through a
// remote: one was named, or the folder was attached to one before. A folder
// otherwise runs on this device, even when remotes are configured.
func workspaceUsesRemote(c Config, root, remoteFlag string, runRemote bool) bool {
	if remoteFlag != "" || runRemote {
		return true
	}
	_, attached := c.Bindings[root]
	return attached
}

// openLocalWorkspace runs a session on this device, with no remote involved.
func openLocalWorkspace(c Config, o localOpen) error {
	if o.worktree {
		return fmt.Errorf("--worktree needs a remote; attach this folder with jara --remote NAME")
	}
	agent, err := resolveClientAgent(c, o.root, "", o.action, o.agent)
	if err != nil {
		return err
	}
	if o.action == "handoff" && agent == "" {
		agent = otherAgent(defaultLocalAgent(c, o.root))
	}
	cwd := filepath.Join(o.root, filepath.FromSlash(o.rel))
	q := Request{Runtime: agent, AgentExplicit: o.agent != "", CodexArgs: o.codexArgs, Subdir: filepath.ToSlash(o.rel)}
	switch o.action {
	case "run":
		q.New, q.Background, q.Prompt, q.ResumeLatest = true, true, o.prompt, o.resumeLatest
		return startLocalBackground(q, cwd)
	case "handoff":
		q.New, q.Handoff, q.Prompt = true, true, o.note
		return runLocalNative(q, cwd)
	}
	q.New = o.action == "new"
	q.Resume = o.action == "resume" || o.action == "sessions"
	q.ResumeLatest = o.action == "resume" && o.latest
	if o.chatInterface != "jara" || runtime.GOOS == "windows" {
		return runLocalNative(q, cwd)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		// The Jara interface runs over Codex's app-server; Claude Code alone
		// still works in its own terminal.
		if _, claudeErr := exec.LookPath("claude"); claudeErr == nil && o.agent == "" {
			q.Runtime = protocol.AgentClaude
		}
		if q.Runtime == protocol.AgentClaude {
			return runLocalNative(q, cwd)
		}
	}
	return openLocalRunner(localRunnerOpen{root: o.root}, q)
}

// defaultLocalAgent is the agent a folder uses when none is named.
func defaultLocalAgent(c Config, root string) string {
	agent, err := resolveClientAgent(c, root, "", "open", "")
	if err != nil || agent == "" {
		return protocol.AgentCodex
	}
	return agent
}

// localSetup is `jara setup` for this device alone: Jara needs nothing but
// the agent CLIs and their own sign-in.
func localSetup() error {
	found := false
	for _, agent := range []struct{ binary, name, login string }{
		{"codex", "Codex", "codex login"},
		{"claude", "Claude Code", "claude (then /login)"},
	} {
		if _, err := exec.LookPath(agent.binary); err != nil {
			fmt.Printf("- %s: not installed\n", agent.name)
			continue
		}
		found = true
		fmt.Printf("- %s: installed; sign in with %s if you have not yet\n", agent.name, agent.login)
	}
	if !found {
		return fmt.Errorf("install the Codex or Claude Code CLI, then run jara in a project folder")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		fmt.Println("The Jara conversation interface needs Codex; with Claude Code alone, use jara --interface codex --agent claude.")
	}
	fmt.Println("Ready: run jara in a project folder. Add a remote later with jara setup for device handoffs and phone access.")
	return nil
}
