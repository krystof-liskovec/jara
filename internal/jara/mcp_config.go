package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// Each host keeps the share of the client's shared MCP list that runs there
// (see mcp_config_command.go) and hands it to whichever agent it starts: Claude Code reads a generated --mcp-config file and Codex
// gets -c mcp_servers.NAME overrides. The agents' own configuration files are
// left alone. Codex overrides sit on its command line, so header and env
// values there are visible to other processes on the same host.

// mcpServer is one server: a command for stdio, or a URL for streamable HTTP.
type mcpServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type mcpServers map[string]mcpServer

const mcpServerLimit = 64

var mcpNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func mcpConfigPath() string { return filepath.Join(fsutil.DataRoot(), "mcp-servers.json") }

// mcpClaudeConfigPath is the file Claude Code receives with --mcp-config.
func mcpClaudeConfigPath() string { return filepath.Join(fsutil.DataRoot(), "mcp-claude.json") }

func validateMCPServers(servers mcpServers) error {
	if len(servers) > mcpServerLimit {
		return fmt.Errorf("at most %d MCP servers are allowed", mcpServerLimit)
	}
	for name, server := range servers {
		switch {
		case !mcpNameRE.MatchString(name):
			return fmt.Errorf("MCP server name %q may use only letters, digits, - and _", name)
		case name == "jara":
			return fmt.Errorf("the name jara belongs to Jara's delegation server")
		case (server.Command == "") == (server.URL == ""):
			return fmt.Errorf("MCP server %s needs either a command or a URL", name)
		case server.URL != "" && (len(server.Args) > 0 || len(server.Env) > 0):
			return fmt.Errorf("MCP server %s has a URL, so it takes headers instead of arguments or env", name)
		case server.Command != "" && len(server.Headers) > 0:
			return fmt.Errorf("MCP server %s runs a command, so it takes env instead of headers", name)
		case server.URL != "" && !strings.HasPrefix(server.URL, "http://") && !strings.HasPrefix(server.URL, "https://"):
			return fmt.Errorf("MCP server %s URL must start with http:// or https://", name)
		}
		for _, value := range append(append([]string{server.Command, server.URL}, server.Args...), mapValues(server.Env, server.Headers)...) {
			if strings.ContainsRune(value, 0) {
				return fmt.Errorf("MCP server %s settings cannot contain NUL bytes", name)
			}
		}
	}
	return nil
}

func mapValues(maps ...map[string]string) []string {
	var values []string
	for _, m := range maps {
		for key, value := range m {
			values = append(values, key, value)
		}
	}
	return values
}

func loadMCPServers() (mcpServers, error) {
	servers := mcpServers{}
	data, err := os.ReadFile(mcpConfigPath())
	if os.IsNotExist(err) {
		return servers, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(data, &servers); err != nil {
		return nil, fmt.Errorf("read %s: %w", mcpConfigPath(), err)
	}
	if servers == nil {
		servers = mcpServers{}
	}
	return servers, validateMCPServers(servers)
}

func saveMCPServers(servers mcpServers) error {
	if err := validateMCPServers(servers); err != nil {
		return err
	}
	if len(servers) == 0 {
		err := os.Remove(mcpConfigPath())
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return fsutil.WriteJSON(mcpConfigPath(), servers)
}

// mcpLaunchArgs returns the arguments that give an agent Jara's MCP servers.
func mcpLaunchArgs(runtime string) ([]string, error) {
	servers, err := loadMCPServers()
	if err != nil || len(servers) == 0 {
		return nil, err
	}
	if runtime == protocol.AgentClaude {
		if err = writeClaudeMCPConfig(servers); err != nil {
			return nil, err
		}
		// The = form: --mcp-config takes several values otherwise.
		return []string{"--mcp-config=" + mcpClaudeConfigPath()}, nil
	}
	return codexMCPOverrides(servers), nil
}

// codexMCPOverrides renders the servers as Codex -c overrides; values are
// TOML, which JSON strings, arrays and quoted keys also are.
func codexMCPOverrides(servers mcpServers) []string {
	var args []string
	set := func(name, key, value string) {
		args = append(args, "-c", "mcp_servers."+name+"."+key+"="+value)
	}
	for _, name := range sortedMCPNames(servers) {
		server := servers[name]
		if server.URL != "" {
			set(name, "url", tomlString(server.URL))
			if len(server.Headers) > 0 {
				set(name, "http_headers", tomlTable(server.Headers))
			}
			continue
		}
		set(name, "command", tomlString(server.Command))
		if len(server.Args) > 0 {
			quoted := make([]string, len(server.Args))
			for i, arg := range server.Args {
				quoted[i] = tomlString(arg)
			}
			set(name, "args", "["+strings.Join(quoted, ",")+"]")
		}
		if len(server.Env) > 0 {
			set(name, "env", tomlTable(server.Env))
		}
	}
	return args
}

func tomlString(value string) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(b.String(), "\n")
}

func tomlTable(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, key := range keys {
		pairs[i] = tomlString(key) + "=" + tomlString(values[key])
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func sortedMCPNames(servers mcpServers) []string {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// writeClaudeMCPConfig writes the servers in Claude Code's mcpServers format,
// leaving the file alone when it is already current.
func writeClaudeMCPConfig(servers mcpServers) error {
	entries := map[string]any{}
	for name, server := range servers {
		if server.URL != "" {
			entries[name] = map[string]any{"type": "http", "url": server.URL, "headers": nonNilMap(server.Headers)}
		} else {
			entries[name] = map[string]any{"type": "stdio", "command": server.Command, "args": nonNilSlice(server.Args), "env": nonNilMap(server.Env)}
		}
	}
	config := map[string]any{"mcpServers": entries}
	want, _ := json.MarshalIndent(config, "", "  ")
	if have, err := os.ReadFile(mcpClaudeConfigPath()); err == nil && bytes.Equal(bytes.TrimSpace(have), want) {
		return nil
	}
	return fsutil.WriteJSON(mcpClaudeConfigPath(), config)
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func nonNilSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// mcpRPC serves mcp-set on the host: clients send it their share of the
// shared list.
func mcpRPC(action string, input io.Reader) error {
	switch action {
	case "set":
		raw, err := io.ReadAll(io.LimitReader(input, 1<<20+1))
		if err != nil {
			return err
		}
		if len(raw) > 1<<20 {
			return fmt.Errorf("MCP server list exceeds 1 MiB")
		}
		servers := mcpServers{}
		if err = json.Unmarshal(raw, &servers); err != nil {
			return fmt.Errorf("MCP server list must be a JSON object of servers: %w", err)
		}
		return saveMCPServers(servers)
	}
	return fmt.Errorf("unknown MCP operation")
}
