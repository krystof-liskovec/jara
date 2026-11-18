package jara

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
)

// jara usage and /usage show the usage limits of every Codex and Claude
// Code account Jara can reach: this device's and each remote's, one host
// after another. Codex answers live; Claude Code only reports usage while
// it runs a turn, so its newest report on each host is shown with its age.

// hostUsage reads this machine's usage limits.
func hostUsage() usageReport {
	var report usageReport
	if binary, ok := installedAgent(protocol.CodexExecutable(), "codex"); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		server, err := startCodexOneShot(ctx, binary)
		if err == nil {
			err = server.request("account/rateLimits/read", nil, &report.Codex)
			server.close()
		}
		if err != nil {
			report.Codex, report.CodexError = nil, "unavailable: "+strings.TrimPrefix(err.Error(), "Codex: ")
		}
	}
	report.Accounts = agentrouter.Accounts()
	usage := agentrouter.ClaudeUsage()
	if _, ok := installedAgent(protocol.ClaudeExecutable(), "claude"); ok || usage["rateLimits"] != nil {
		report.Claude, _ = json.Marshal(usage)
	}
	return report
}

// installedAgent finds an agent at Jara's fixed path or on PATH.
func installedAgent(path, name string) (string, bool) {
	if _, err := os.Stat(path); err == nil {
		return path, true
	}
	found, err := exec.LookPath(name)
	return found, err == nil
}

func (r usageReport) empty() bool {
	return len(r.Codex) == 0 && r.CodexError == "" && len(r.Claude) == 0 && r.ClaudeError == ""
}

// usageCommand runs jara usage [--remote NAME].
func usageCommand(args []string) error {
	only := ""
	switch {
	case len(args) == 2 && args[0] == "--remote":
		only = args[1]
	case len(args) == 1 && strings.HasPrefix(args[0], "--remote="):
		only = strings.TrimPrefix(args[0], "--remote=")
	case len(args) != 0:
		return fmt.Errorf("use jara usage [--remote NAME]")
	}
	c, err := readConfig()
	if err != nil {
		return err
	}
	if _, ok := c.Remotes[only]; only != "" && !ok {
		return fmt.Errorf("there is no remote named %s; see jara remote list", only)
	}
	fmt.Print(collectUsage(c, only))
	return nil
}

// collectUsage asks this device and every remote at once, or only the remote
// named only, and lists them in the remotes' ranking.
func collectUsage(c Config, only string) string {
	places := chatRemotePlaces(c)
	if only != "" {
		places = []string{only}
	}
	reports := make([]string, len(places))
	var wait sync.WaitGroup
	for i, place := range places {
		wait.Go(func() { reports[i] = placeUsage(c, place) })
	}
	wait.Wait()
	var b strings.Builder
	for _, report := range reports {
		if report == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(report)
	}
	if b.Len() == 0 {
		return "Neither Codex nor Claude Code is installed here, and no remotes are configured.\n"
	}
	return b.String()
}

// placeUsage renders one place's usage under its name; "" is this device,
// left out when it runs neither agent.
func placeUsage(c Config, place string) string {
	if place == "" {
		report := hostUsage()
		if report.empty() {
			return ""
		}
		return "This device\n" + indentUsage(formatUsage(report, time.Now()))
	}
	var report usageReport
	if err := rpc(c.Remotes[place], Request{Op: "usage"}, nil, &report); err != nil {
		if strings.Contains(err.Error(), "unknown RPC operation") {
			return place + " · runs an older Jara; update it with jara deployment upgrade\n"
		}
		return place + " · unreachable: " + firstLine(err.Error()) + "\n"
	}
	return place + "\n" + indentUsage(formatUsage(report, time.Now()))
}

func indentUsage(text string) string {
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "  " + line
		}
	}
	return strings.Join(lines, "")
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return line
}

// usageRPC answers a client's usage op on a host.
func usageRPC(out io.Writer) error {
	return json.NewEncoder(out).Encode(hostUsage())
}
