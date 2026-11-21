package jara

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// When Jara opens a conversation it offers updates: Jara on remotes older
// than this client, and newer Codex and Claude Code releases for this device
// and the remotes, counting only agents signed in on each machine. Release
// and remote versions come from a check cached for a few hours, so opening
// does not wait on the network; the comparison with this client's versions
// is made on every start, so a remote left behind after this device updated
// is offered again.

// machineVersions is what a machine runs and which agents are signed in.
type machineVersions struct {
	Jara       string     `json:"jara"`
	Build      buildStamp `json:"build,omitzero"`
	Codex      string     `json:"codex,omitempty"`
	Claude     string     `json:"claude,omitempty"`
	CodexAuth  bool       `json:"codexAuth,omitempty"`
	ClaudeAuth bool       `json:"claudeAuth,omitempty"`
}

type remoteVersions struct {
	machineVersions
	Partial bool   `json:"partial,omitempty"` // an older Jara told only its own version
	Error   string `json:"error,omitempty"`
}

type updateCheck struct {
	CheckedAt int64                     `json:"checkedAt"`
	Latest    map[string]string         `json:"latest"` // newest Codex and Claude Code releases
	Remotes   map[string]remoteVersions `json:"remotes"`
	Snoozed   map[string]int64          `json:"snoozed,omitempty"` // declined offers, until
}

const updateCheckEvery = 6 * time.Hour

func updateCheckPath() string { return filepath.Join(fsutil.DataRoot(), "update-check.json") }

func loadUpdateCheck() updateCheck {
	var check updateCheck
	if data, err := os.ReadFile(updateCheckPath()); err == nil {
		_ = json.Unmarshal(data, &check)
	}
	if check.Latest == nil {
		check.Latest = map[string]string{}
	}
	if check.Remotes == nil {
		check.Remotes = map[string]remoteVersions{}
	}
	if check.Snoozed == nil {
		check.Snoozed = map[string]int64{}
	}
	return check
}

var updateCheckMu sync.Mutex

// saveUpdateCheck keeps the snoozes recorded since check was loaded.
func saveUpdateCheck(check updateCheck) {
	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()
	for key, until := range loadUpdateCheck().Snoozed {
		if check.Snoozed[key] < until {
			check.Snoozed[key] = until
		}
	}
	_ = fsutil.WriteJSON(updateCheckPath(), check)
}

// stale reports whether c's check is old or misses one of its remotes.
func (check updateCheck) stale(c Config, now time.Time) bool {
	if now.Sub(time.Unix(check.CheckedAt, 0)) > updateCheckEvery {
		return true
	}
	for name := range c.Remotes {
		if _, ok := check.Remotes[name]; !ok {
			return true
		}
	}
	return false
}

// refreshUpdateCheck asks npm for the newest releases and every remote for
// its versions, all at once.
func refreshUpdateCheck(c Config, check updateCheck) updateCheck {
	var mu sync.Mutex
	var wait sync.WaitGroup
	latest := map[string]func() (string, error){protocol.AgentCodex: latestCodex, protocol.AgentClaude: latestClaudeCode}
	for agent, lookup := range latest {
		wait.Go(func() {
			if version, err := lookup(); err == nil {
				mu.Lock()
				check.Latest[agent] = version
				mu.Unlock()
			}
		})
	}
	remotes := map[string]remoteVersions{}
	for name, remote := range c.Remotes {
		wait.Go(func() {
			versions := fetchRemoteVersions(remote)
			mu.Lock()
			remotes[name] = versions
			mu.Unlock()
		})
	}
	wait.Wait()
	check.Remotes, check.CheckedAt = remotes, time.Now().Unix()
	return check
}

// rememberUpgradedRemote records what name runs after an upgrade, so the
// cached check does not offer the same upgrade again.
func rememberUpgradedRemote(c Config, name string) {
	remote, ok := c.Remotes[name]
	if !ok {
		return
	}
	check := loadUpdateCheck()
	check.Remotes[name] = fetchRemoteVersions(remote)
	saveUpdateCheck(check)
}

func fetchRemoteVersions(remote Remote) remoteVersions {
	var versions remoteVersions
	err := rpc(remote, Request{Op: "versions"}, nil, &versions.machineVersions)
	if err != nil && strings.Contains(err.Error(), "unknown RPC operation") {
		var old struct{ Version string }
		if err = rpc(remote, Request{Op: "client-version"}, nil, &old); err == nil {
			versions.Jara, versions.Partial = old.Version, true
		}
	}
	if err != nil {
		versions.Error = firstLine(err.Error())
	}
	return versions
}

// thisMachineVersions reads the versions and sign-ins here; on a host it
// answers the versions op.
func thisMachineVersions() machineVersions {
	versions := machineVersions{Jara: Version, Build: thisBuild()}
	var wait sync.WaitGroup
	if codex, ok := installedAgent(protocol.CodexExecutable(), "codex"); ok {
		wait.Go(func() {
			versions.Codex = agentVersion(codex)
			versions.CodexAuth = exec.Command(codex, "login", "status").Run() == nil
		})
	}
	if claude, ok := installedAgent(protocol.ClaudeExecutable(), "claude"); ok {
		wait.Go(func() { versions.Claude = agentVersion(claude) })
		versions.ClaudeAuth = claudeCredentialsPresent()
	}
	wait.Wait()
	return versions
}

var releasePattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// agentVersion reads an agent CLI's x.y.z version.
func agentVersion(binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "--version").Output()
	if err != nil {
		return ""
	}
	return releasePattern.FindString(string(out))
}

func versionsRPC() error {
	return json.NewEncoder(os.Stdout).Encode(thisMachineVersions())
}
