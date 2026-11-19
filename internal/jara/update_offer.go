package jara

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
	"krystof.liskovec/jara/internal/protocol"
)

const updateSnooze = 24 * time.Hour // "not now" holds back the same offer this long

var updateAgents = []struct{ id, name string }{{protocol.AgentCodex, "Codex"}, {protocol.AgentClaude, "Claude Code"}}

type agentUpdate struct {
	agent, name, target string
	local               string            // this device's version when it is behind; "" otherwise
	remotes             map[string]string // remotes behind, with their version
}

type updatePlan struct {
	jara     map[string]bool // remotes running an older Jara: true when Jara can upgrade them
	agents   []agentUpdate
	snoozeIn []string // keys of the offers made, held back when declined
}

func (p updatePlan) empty() bool { return len(p.jara) == 0 && len(p.agents) == 0 }

// planUpdates compares this client, this device and the remotes with the
// newest releases, leaving out snoozed offers and agents not signed in.
func planUpdates(c Config, check updateCheck, here machineVersions, now time.Time) updatePlan {
	plan := updatePlan{jara: map[string]bool{}}
	awake := func(key string) bool { return check.Snoozed[key] <= now.Unix() }
	for _, name := range sortedRemoteNames(c.Remotes) {
		remote, ok := check.Remotes[name]
		older := ok && remote.Error == "" && (remote.Partial || versionNewer(Version, remote.Jara) || remote.Build.olderThan(thisBuild()))
		if key := "jara/" + name + "/" + Version + "/" + thisBuild().Source; older && awake(key) {
			_, managed := c.HostDeployments[name]
			plan.jara[name] = managed
			plan.snoozeIn = append(plan.snoozeIn, key)
		}
	}
	for _, agent := range updateAgents {
		local, localAuth := here.Codex, here.CodexAuth
		if agent.id == protocol.AgentClaude {
			local, localAuth = here.Claude, here.ClaudeAuth
		}
		target := check.Latest[agent.id]
		if versionNewer(local, target) || target == "" {
			target = local // this device may be ahead of the last release check
		}
		if target == "" {
			continue
		}
		update := agentUpdate{agent: agent.id, name: agent.name, target: target, remotes: map[string]string{}}
		if key := agent.id + "/local/" + target; local != "" && localAuth && versionNewer(target, local) && awake(key) {
			update.local = local
			plan.snoozeIn = append(plan.snoozeIn, key)
		}
		for name, remote := range check.Remotes {
			version, auth := remote.Codex, remote.CodexAuth
			if agent.id == protocol.AgentClaude {
				version, auth = remote.Claude, remote.ClaudeAuth
			}
			if _, managed := c.HostDeployments[name]; !managed || remote.Error != "" || !auth || version == "" || !versionNewer(target, version) {
				continue
			}
			if key := agent.id + "/" + name + "/" + target; awake(key) {
				update.remotes[name] = version
				plan.snoozeIn = append(plan.snoozeIn, key)
			}
		}
		if update.local != "" || len(update.remotes) > 0 {
			plan.agents = append(plan.agents, update)
		}
	}
	return plan
}

// updateChoices is what the user accepted.
type updateChoices struct {
	local   []string                   // agents to update on this device
	remotes map[string]map[string]bool // remote → "jara", agent IDs
	snooze  []string
}

// askUpdates asks about each offer; a declined one is held back for a day.
func askUpdates(plan updatePlan, in *bufio.Reader, out io.Writer) (updateChoices, error) {
	choices := updateChoices{remotes: map[string]map[string]bool{}}
	accept := func(remote, what string) {
		if choices.remotes[remote] == nil {
			choices.remotes[remote] = map[string]bool{}
		}
		choices.remotes[remote][what] = true
	}
	declined := map[string]bool{}
	var managed, unmanaged []string
	for _, name := range sortedKeys(plan.jara) {
		if plan.jara[name] {
			managed = append(managed, name)
		} else {
			unmanaged = append(unmanaged, name)
		}
	}
	if len(unmanaged) > 0 {
		fmt.Fprintf(out, "%s %s an older Jara than this client. Jara has no Docker host connection to update %s; rerun jara setup for %s to add one.\n",
			strings.Join(unmanaged, ", "), plural(len(unmanaged), "runs", "run"), plural(len(unmanaged), "it", "them"), plural(len(unmanaged), "it", "each"))
	}
	if len(managed) > 0 {
		yes, err := askUpdateYes(in, out, strings.Join(managed, ", ")+" "+plural(len(managed), "runs", "run")+" an older Jara than this client. Update "+plural(len(managed), "it", "them")+" now?", true)
		if err != nil {
			return choices, err
		}
		for _, name := range managed {
			if yes {
				accept(name, "jara")
			} else {
				declined["jara/"+name] = true
			}
		}
	}
	for _, update := range plan.agents {
		remotes := sortedKeys(update.remotes)
		var behind []string
		for _, name := range remotes {
			behind = append(behind, name+" "+update.remotes[name])
		}
		local, everywhere := false, false
		switch {
		case update.local != "" && len(remotes) > 0:
			fmt.Fprintf(out, "%s %s is out; this device has %s, %s.\n  1) Update this device and the remotes\n  2) Update this device only\n  3) Not now\n",
				update.name, update.target, update.local, strings.Join(behind, ", "))
			choice, err := askChoice(in, out, 3)
			if err != nil {
				return choices, err
			}
			local, everywhere = choice <= 2, choice == 1
		case update.local != "":
			yes, err := askUpdateYes(in, out, fmt.Sprintf("%s %s is out; this device has %s. Update it now?", update.name, update.target, update.local), true)
			if err != nil {
				return choices, err
			}
			local = yes
		default:
			yes, err := askUpdateYes(in, out, fmt.Sprintf("%s runs older %s than this device (%s). Update %s?", strings.Join(behind, ", "), update.name, update.target, plural(len(remotes), "it", "them")), true)
			if err != nil {
				return choices, err
			}
			everywhere = yes
		}
		if local {
			choices.local = append(choices.local, update.agent)
		} else if update.local != "" {
			declined[update.agent+"/local"] = true
		}
		for _, name := range remotes {
			if everywhere {
				accept(name, update.agent)
			} else {
				declined[update.agent+"/"+name] = true
			}
		}
	}
	for _, key := range plan.snoozeIn {
		parts := strings.SplitN(key, "/", 3)
		if declined[parts[0]+"/"+parts[1]] {
			choices.snooze = append(choices.snooze, key)
		}
	}
	return choices, nil
}

func askUpdateYes(in *bufio.Reader, out io.Writer, prompt string, defaultYes bool) (bool, error) {
	for {
		fmt.Fprint(out, prompt+" [Y/n]: ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(out, "Answer y or n.")
	}
}

func askChoice(in *bufio.Reader, out io.Writer, count int) (int, error) {
	for {
		fmt.Fprint(out, "Choose [1]: ")
		line, err := in.ReadString('\n')
		if err != nil && line == "" {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return 1, nil
		}
		if n, convErr := strconv.Atoi(line); convErr == nil && n >= 1 && n <= count {
			return n, nil
		}
		fmt.Fprintf(out, "Enter a number from 1 to %d.\n", count)
	}
}

func plural(count int, one, many string) string {
	if count == 1 {
		return one
	}
	return many
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// runUpdates updates this device's agents with their own updaters, then
// upgrades each chosen remote once, raising only the agents accepted there.
func runUpdates(c Config, choices updateChoices, out io.Writer) []string {
	var updated []string
	for _, agent := range choices.local {
		binary, name := protocol.CodexExecutable(), "codex"
		if agent == protocol.AgentClaude {
			binary, name = protocol.ClaudeExecutable(), "claude"
		}
		path, ok := installedAgent(binary, name)
		if !ok {
			continue
		}
		fmt.Fprintf(out, "Updating %s on this device…\n", name)
		cmd := exec.Command(path, "update")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(out, "%s update failed: %v\n", name, err)
		}
	}
	for _, name := range sortedKeys(choices.remotes) {
		deployment, ok := c.HostDeployments[name]
		if !ok {
			continue
		}
		accepted := choices.remotes[name]
		options := deploymentUpgradeOptions{assumeYes: true, keepCodexVersion: !accepted[protocol.AgentCodex], keepClaudeVersion: !accepted[protocol.AgentClaude]}
		fmt.Fprintf(out, "Upgrading %s…\n", name)
		if err := upgradeGuidedHost(name, deployment, deploymentBundle(), Version, options, out); err != nil {
			fmt.Fprintf(out, "%s was not upgraded: %v\n", name, err)
			continue
		}
		updated = append(updated, name)
	}
	return updated
}

// checkUpdates refreshes the check when due (now, with force), then offers
// what is newer. At start it waits briefly for the refresh and otherwise
// lets it finish in the background for the next start.
func checkUpdates(c Config, force bool, in *bufio.Reader, out io.Writer) error {
	check := loadUpdateCheck()
	if force {
		check.Snoozed = map[string]int64{}
		check = refreshUpdateCheck(c, check)
		saveUpdateCheck(check)
	} else if check.stale(c, time.Now()) {
		fresh := make(chan updateCheck, 1)
		go func() {
			refreshed := refreshUpdateCheck(c, loadUpdateCheck())
			saveUpdateCheck(refreshed)
			fresh <- refreshed
		}()
		select {
		case check = <-fresh:
		case <-time.After(3 * time.Second):
		}
	}
	if len(check.Latest) == 0 && len(check.Remotes) == 0 {
		return nil
	}
	plan := planUpdates(c, check, thisMachineVersions(), time.Now())
	if plan.empty() {
		if force {
			fmt.Fprintln(out, "Jara, Codex and Claude Code are up to date on this device and the remotes Jara reached.")
		}
		return nil
	}
	choices, err := askUpdates(plan, in, out)
	if errors.Is(err, io.EOF) { // no answer: leave everything as it is
		fmt.Fprintln(out)
		return nil
	}
	if err != nil {
		return err
	}
	if len(choices.snooze) > 0 {
		until := time.Now().Add(updateSnooze).Unix()
		for _, key := range choices.snooze {
			check.Snoozed[key] = until
		}
		saveUpdateCheck(check)
	}
	if len(choices.local) == 0 && len(choices.remotes) == 0 {
		return nil
	}
	updated := runUpdates(c, choices, out)
	for _, name := range updated { // what they run now, so they are not offered again
		check.Remotes[name] = fetchRemoteVersions(c.Remotes[name])
	}
	saveUpdateCheck(check)
	return nil
}

// offerUpdatesAtStart offers updates when an interactive conversation opens.
func offerUpdatesAtStart(c Config) {
	if os.Getenv("JARA_NO_UPDATE_CHECK") != "" || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	if err := checkUpdates(c, false, bufio.NewReader(os.Stdin), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "Update check:", err)
	}
}
