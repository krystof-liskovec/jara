package jara

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

// The shared MCP list lives in this device's client configuration. Each
// server runs everywhere unless limited to some places: "local" is this
// device, other names are remotes. This device's share is written where
// local agents read it; each remote is sent its share after every change and,
// when it changed since, before a session opens there.

// sharedMCPServer is one server of the shared list and where it runs.
type sharedMCPServer struct {
	mcpServer
	On []string `json:"on,omitempty"` // empty: everywhere
}

const mcpLocal = "local"

const mcpUsage = "use jara config mcp list | add NAME [--on PLACE]... (--url URL [--header K=V]... | [--env K=V]... -- COMMAND [ARGS...]) | where NAME everywhere|PLACE... | remove NAME | import [--from claude|codex] [--on PLACE]... | sync"

func mcpConfigCommand(c *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", mcpUsage)
	}
	action, rest := args[0], args[1:]
	name := ""
	if action == "add" || action == "remove" || action == "where" {
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return fmt.Errorf("%s", mcpUsage)
		}
		name, rest = rest[0], rest[1:]
	}
	if action == "where" {
		return mcpWhere(c, name, rest)
	}
	var command []string
	if action == "add" {
		rest, command = splitCodexArgs(rest)
	}
	set := flag.NewFlagSet("config mcp "+action, flag.ContinueOnError)
	url := set.String("url", "", "streamable HTTP server URL")
	from := set.String("from", "", "import only from claude or codex")
	var env, headers, on codexArgList
	set.Var(&env, "env", "KEY=VALUE for a command server, repeatable")
	set.Var(&headers, "header", "KEY=VALUE for a URL server, repeatable")
	set.Var(&on, "on", "run only here: local or a remote name, repeatable")
	if err := set.Parse(rest); err != nil {
		return err
	}
	if set.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q; put the server command after --", set.Arg(0))
	}
	if err := checkMCPPlaces(c, on); err != nil {
		return err
	}
	if c.MCPServers == nil {
		c.MCPServers = map[string]sharedMCPServer{}
	}
	switch action {
	case "list":
		printMCPServers(c.MCPServers)
		return nil
	case "sync":
		return applyMCPServers(c, true)
	case "add":
		server := mcpServer{URL: *url}
		if len(command) > 0 {
			server.Command, server.Args = command[0], command[1:]
		}
		var err error
		if server.Env, err = keyValues("--env", env); err != nil {
			return err
		}
		if server.Headers, err = keyValues("--header", headers); err != nil {
			return err
		}
		c.MCPServers[name] = sharedMCPServer{mcpServer: server, On: on}
	case "remove":
		if _, ok := c.MCPServers[name]; !ok {
			return fmt.Errorf("there is no MCP server named %s", name)
		}
		delete(c.MCPServers, name)
	case "import":
		imported, notes := importLocalMCPServers(*from)
		for _, note := range notes {
			fmt.Println(note)
		}
		if len(imported) == 0 {
			return fmt.Errorf("found no MCP servers to import")
		}
		for _, importedName := range sortedMCPNames(imported) {
			c.MCPServers[importedName] = sharedMCPServer{mcpServer: imported[importedName], On: on}
		}
		fmt.Printf("Imported %s: %s\n", mcpCount(len(imported)), strings.Join(sortedMCPNames(imported), ", "))
	default:
		return fmt.Errorf("%s", mcpUsage)
	}
	if err := validateMCPServers(mcpShare(c.MCPServers, "")); err != nil {
		return err
	}
	return applyMCPServers(c, false)
}

// mcpWhere changes where a server runs.
func mcpWhere(c *Config, name string, places []string) error {
	server, ok := c.MCPServers[name]
	if !ok {
		return fmt.Errorf("there is no MCP server named %s", name)
	}
	if len(places) == 0 {
		return fmt.Errorf("use jara config mcp where %s everywhere | local | REMOTE...", name)
	}
	if len(places) == 1 && places[0] == "everywhere" {
		places = nil
	} else if err := checkMCPPlaces(c, places); err != nil {
		return err
	}
	server.On = places
	c.MCPServers[name] = server
	return applyMCPServers(c, false)
}

func checkMCPPlaces(c *Config, places []string) error {
	for _, place := range places {
		if _, ok := c.Remotes[place]; place != mcpLocal && !ok {
			return fmt.Errorf("unknown place %q: use local for this device or a remote name (jara remote list)", place)
		}
	}
	return nil
}

// mcpShare returns the servers that run at place; "" returns them all.
func mcpShare(servers map[string]sharedMCPServer, place string) mcpServers {
	share := mcpServers{}
	for name, server := range servers {
		if place == "" || len(server.On) == 0 || slices.Contains(server.On, place) {
			share[name] = server.mcpServer
		}
	}
	return share
}

func mcpShareDigest(share mcpServers) string {
	data, _ := json.Marshal(share) // map keys marshal sorted
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// applyMCPServers writes this device's share, sends every remote its share
// (with force, also unchanged ones), then saves the shared list and what each
// remote received. Unreachable remotes catch up before their next session.
func applyMCPServers(c *Config, force bool) error {
	local := mcpShare(c.MCPServers, mcpLocal)
	if err := saveMCPServers(local); err != nil {
		return fmt.Errorf("save this device's MCP servers: %w", err)
	}
	fmt.Printf("This device: %s\n", mcpCount(len(local)))
	for _, name := range sortedRemoteNames(c.Remotes) {
		share := mcpShare(c.MCPServers, name)
		if !force && c.MCPSynced[name] == mcpShareDigest(share) {
			fmt.Printf("%s: %s, up to date\n", name, mcpCount(len(share)))
			continue
		}
		if err := sendMCPShare(c, name, share); err != nil {
			fmt.Printf("%s: not updated yet (%v); it catches up before its next session\n", name, err)
			continue
		}
		fmt.Printf("%s: %s\n", name, mcpCount(len(share)))
	}
	return saveConfig(*c)
}

func sendMCPShare(c *Config, name string, share mcpServers) error {
	raw, _ := json.Marshal(share)
	if err := rpc(c.Remotes[name], Request{Op: "mcp-set"}, bytes.NewReader(raw), nil); err != nil {
		return err
	}
	if c.MCPSynced == nil {
		c.MCPSynced = map[string]string{}
	}
	c.MCPSynced[name] = mcpShareDigest(share)
	return nil
}

// syncMCPToRemote sends a remote its share before a session opens there,
// when it changed since the remote last received it. It only warns.
func syncMCPToRemote(r Remote) {
	c, err := readConfig()
	if err != nil || len(c.MCPServers) == 0 && len(c.MCPSynced) == 0 {
		return
	}
	for _, name := range sortedRemoteNames(c.Remotes) {
		if c.Remotes[name] != r {
			continue
		}
		share := mcpShare(c.MCPServers, name)
		if c.MCPSynced[name] == mcpShareDigest(share) {
			return
		}
		if err = sendMCPShare(&c, name, share); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: could not send MCP servers to %s: %v\n", name, err)
			return
		}
		_ = saveConfig(c)
		return
	}
}

func sortedRemoteNames(remotes map[string]Remote) []string {
	return slices.Sorted(maps.Keys(remotes))
}

func keyValues(option string, values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("%s takes KEY=VALUE, got %q", option, value)
		}
		out[key] = val
	}
	return out, nil
}

func mcpCount(count int) string {
	if count == 1 {
		return "1 MCP server"
	}
	return fmt.Sprintf("%d MCP servers", count)
}

// printMCPServers lists the servers and where they run; env and header
// values stay hidden as they often hold tokens.
func printMCPServers(servers map[string]sharedMCPServer) {
	if len(servers) == 0 {
		fmt.Println("No MCP servers yet · add one with jara config mcp add, or import yours with jara config mcp import")
		return
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		server := servers[name]
		target, keys, kind := server.URL, server.Headers, "headers"
		if server.Command != "" {
			target, keys, kind = strings.Join(append([]string{server.Command}, server.Args...), " "), server.Env, "env"
		}
		where := "everywhere"
		if len(server.On) > 0 {
			where = "on " + strings.Join(server.On, ", ")
		}
		line := "  " + name + "  " + target + "  · " + where
		if len(keys) > 0 {
			line += " · " + kind + ": " + strings.Join(slices.Sorted(maps.Keys(keys)), ", ")
		}
		fmt.Println(line)
	}
}
