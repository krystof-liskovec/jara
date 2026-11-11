package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

const jaraBinary = "/usr/local/bin/jara"

// delegationParent finds the task that started this MCP server, if any.
// Codex passes only an allow-list of environment variables to MCP servers, so
// the ancestor processes' environments are checked as well.
func delegationParent() (string, int) {
	id := os.Getenv("JARA_TASK_ID")
	pid := os.Getppid()
	for hops := 0; id == "" && pid > 1 && hops < 12; hops++ {
		if environ, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "environ")); err == nil {
			for _, entry := range bytes.Split(environ, []byte{0}) {
				if value, ok := bytes.CutPrefix(entry, []byte("JARA_TASK_ID=")); ok {
					id = string(value)
					break
				}
			}
		}
		pid = parentPID(pid)
	}
	if id == "" {
		return "", 1
	}
	parent, err := loadTask(id)
	if err != nil {
		return "", 1
	}
	return parent.ID, parent.Depth + 1
}

func parentPID(pid int) int {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0
	}
	// The command name may contain spaces; fields after its closing ")".
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 {
		return 0
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 2 {
		return 0
	}
	ppid, _ := strconv.Atoi(fields[1])
	return ppid
}

func delegationDisabledMarker() string {
	return filepath.Join(fsutil.DataRoot(), "delegation-disabled")
}

func codexConfigPath() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		user, _ := os.UserHomeDir()
		home = filepath.Join(user, ".codex")
	}
	return filepath.Join(home, "config.toml")
}

const codexMCPBlock = `
# Added by Jara: lets Codex delegate tasks to Claude Code or Codex.
# Remove with: jara delegation disable
[mcp_servers.jara]
command = "/usr/local/bin/jara"
args = ["mcp"]
startup_timeout_sec = 30
tool_timeout_sec = 3600
`

// ensureDelegationRegistered adds Jara's MCP server to both agents' user
// configuration once. Existing entries named jara are never modified, and
// `jara delegation disable` keeps them from being re-added. Failures only
// warn: delegation is optional and must not block a session.
func ensureDelegationRegistered() {
	if _, err := os.Stat(jaraBinary); err != nil {
		return
	}
	if _, err := os.Stat(delegationDisabledMarker()); err == nil {
		return
	}
	if err := registerCodexMCP(); err != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not register Jara delegation with Codex:", err)
	}
	if _, err := os.Stat(protocol.ClaudeBinary); err == nil {
		if err = registerClaudeMCP(); err != nil {
			fmt.Fprintln(os.Stderr, "Warning: could not register Jara delegation with Claude Code:", err)
		}
	}
}

func registerCodexMCP() error {
	path := codexConfigPath()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if bytes.Contains(data, []byte("mcp_servers.jara")) {
		return nil
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	out := append([]byte{}, data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return writePrivateFile(path, append(out, codexMCPBlock...))
}

func unregisterCodexMCP() error {
	path := codexConfigPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte(codexMCPBlock)) {
		if bytes.Contains(data, []byte("mcp_servers.jara")) {
			return fmt.Errorf("%s has a modified [mcp_servers.jara] entry; remove it manually", path)
		}
		return nil
	}
	return writePrivateFile(path, bytes.Replace(data, []byte(codexMCPBlock), nil, 1))
}

func claudeMCPEntry() map[string]any {
	return map[string]any{"type": "stdio", "command": jaraBinary, "args": []any{"mcp"}, "env": map[string]any{}}
}

func updateClaudeSettings(change func(map[string]any) bool) error {
	path := claudeConfigPath()
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("%s is not valid JSON; left unchanged", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if !change(settings) {
		return nil
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writePrivateFile(path, append(encoded, '\n'))
}

func registerClaudeMCP() error {
	return updateClaudeSettings(func(settings map[string]any) bool {
		servers, _ := settings["mcpServers"].(map[string]any)
		if servers == nil {
			servers = map[string]any{}
		}
		if _, exists := servers["jara"]; exists {
			return false
		}
		servers["jara"] = claudeMCPEntry()
		settings["mcpServers"] = servers
		return true
	})
}

func unregisterClaudeMCP() error {
	return updateClaudeSettings(func(settings map[string]any) bool {
		servers, _ := settings["mcpServers"].(map[string]any)
		entry, ok := servers["jara"].(map[string]any)
		if !ok || entry["command"] != jaraBinary {
			return false
		}
		delete(servers, "jara")
		return true
	})
}

// delegationCommand implements `jara delegation enable|disable|status` on
// the host, and is reachable from clients through `jara config delegation`.
func delegationCommand(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("use jara delegation enable|disable|status")
	}
	switch args[0] {
	case "enable":
		if err := os.Remove(delegationDisabledMarker()); err != nil && !os.IsNotExist(err) {
			return err
		}
		ensureDelegationRegistered()
		fmt.Println("Agent delegation enabled; new Codex and Claude Code sessions can use the jara MCP tools.")
	case "disable":
		if err := os.MkdirAll(fsutil.DataRoot(), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(delegationDisabledMarker(), nil, 0600); err != nil {
			return err
		}
		if err := unregisterCodexMCP(); err != nil {
			return err
		}
		if err := unregisterClaudeMCP(); err != nil {
			return err
		}
		fmt.Println("Agent delegation disabled; restart running agent sessions to drop the tools.")
	case "status":
		if _, err := os.Stat(delegationDisabledMarker()); err == nil {
			fmt.Println("Agent delegation: disabled")
		} else {
			fmt.Println("Agent delegation: enabled")
		}
		tasks, _ := listTasks()
		for i, task := range tasks {
			if i == 10 {
				break
			}
			fmt.Println("  " + describeTask(task, false))
		}
	default:
		return fmt.Errorf("use jara delegation enable|disable|status")
	}
	return nil
}
