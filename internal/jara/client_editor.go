package jara

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// openEditorOrTools opens a configured editor or runs project tool setup for
// an associated workspace.
func openEditorOrTools(c Config, r Remote, chosen string, b Binding, rel, action, toolsAction, editor string) error {
	var info Workspace
	if err := rpc(r, Request{Op: "info", ID: b.Workspace}, nil, &info); err != nil {
		return err
	}
	selected := editor
	if selected == "" {
		selected = r.Editor
	}
	if selected == "" {
		selected = c.Editor
	}
	if selected == "" {
		selected = "code"
	}
	remotePath := info.Path
	if rel != "." {
		remotePath += "/" + filepath.ToSlash(rel)
	}
	if action == "tools" {
		return interactive(sshCommand(r, toolsAction == "install", shellArgs("jara", "_tools", toolsAction, remotePath)))
	}
	if err := interactive(sshCommand(r, true, shellArgs("jara", "_tools", "install", remotePath))); err != nil {
		return err
	}
	if selected == "vim" || selected == "nvim" {
		return interactive(sshCommand(r, true, shellArgs("jara", "_tools", "exec", remotePath, selected, "--", remotePath)))
	}
	alias, err := editorAlias(chosen, r)
	if err != nil {
		return err
	}
	if selected != "code" {
		argv, ok := c.EditorCommands[selected]
		if !ok || len(argv) == 0 {
			return fmt.Errorf("unknown editor %q; configure an editor-command argument array", selected)
		}
		expanded := make([]string, len(argv))
		for i, arg := range argv {
			expanded[i] = strings.NewReplacer("{ssh_alias}", alias, "{remote_path}", remotePath).Replace(arg)
		}
		return interactive(exec.Command(expanded[0], expanded[1:]...))
	}
	return interactive(exec.Command("code", "--remote", "ssh-remote+"+alias, remotePath))
}
