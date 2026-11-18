package jara

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
)

// importLocalMCPServers reads the servers this machine's Claude Code and
// Codex know; when both name the same server, Claude Code's entry is kept.
func importLocalMCPServers(from string) (mcpServers, []string) {
	servers := mcpServers{}
	var notes []string
	if from == "" || from == "codex" {
		found, skipped, err := codexMCPServers()
		if err != nil {
			notes = append(notes, "Codex: "+err.Error())
		}
		notes = append(notes, skipped...)
		maps.Copy(servers, found)
	}
	if from == "" || from == "claude" {
		found, skipped, err := claudeMCPServers()
		if err != nil {
			notes = append(notes, "Claude Code: "+err.Error())
		}
		notes = append(notes, skipped...)
		maps.Copy(servers, found)
	}
	delete(servers, "jara") // Jara registers its own delegation server
	return servers, notes
}

func claudeMCPServers() (mcpServers, []string, error) {
	data, err := os.ReadFile(claudeConfigPath())
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var config struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, nil, fmt.Errorf("%s is not valid JSON", claudeConfigPath())
	}
	servers, skipped := mcpServers{}, []string(nil)
	for name, entry := range config.Servers {
		switch {
		case entry.Type == "http" && entry.URL != "":
			servers[name] = mcpServer{URL: entry.URL, Headers: entry.Headers}
		case (entry.Type == "" || entry.Type == "stdio") && entry.Command != "":
			servers[name] = mcpServer{Command: entry.Command, Args: entry.Args, Env: entry.Env}
		default:
			skipped = append(skipped, fmt.Sprintf("Skipped Claude Code's %s: %s servers are not supported", name, textOr(entry.Type, "unknown")))
		}
	}
	return servers, skipped, nil
}

// codexMCPServers asks Codex for its servers, so its TOML need not be parsed.
func codexMCPServers() (mcpServers, []string, error) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		return nil, nil, nil
	}
	out, err := exec.Command(binary, "mcp", "list", "--json").Output()
	if err != nil {
		return nil, nil, fmt.Errorf("codex mcp list failed: %w", err)
	}
	var list []struct {
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		Transport struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"http_headers"`
		} `json:"transport"`
	}
	if err = json.Unmarshal(out, &list); err != nil {
		return nil, nil, fmt.Errorf("codex mcp list --json gave unexpected output")
	}
	servers, skipped := mcpServers{}, []string(nil)
	for _, entry := range list {
		t := entry.Transport
		switch {
		case !entry.Enabled:
			skipped = append(skipped, "Skipped Codex's "+entry.Name+": disabled")
		case t.Type == "streamable_http" && t.URL != "":
			servers[entry.Name] = mcpServer{URL: t.URL, Headers: t.Headers}
		case t.Type == "stdio" && t.Command != "":
			servers[entry.Name] = mcpServer{Command: t.Command, Args: t.Args, Env: t.Env}
		default:
			skipped = append(skipped, fmt.Sprintf("Skipped Codex's %s: %s servers are not supported", entry.Name, textOr(t.Type, "unknown")))
		}
	}
	return servers, skipped, nil
}

func textOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
