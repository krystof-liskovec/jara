package jara

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func doctor(remoteName string, r Remote) error {
	_, err := diagnoseRemote(remoteName, r)
	return err
}

func diagnoseRemote(remoteName string, r Remote) (Diagnosis, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return Diagnosis{}, fmt.Errorf("install the OpenSSH client: %w", err)
	}
	var d Diagnosis
	// The name tells the host what you call it (see chat_host_name.go).
	if err := rpc(r, Request{Op: "doctor", RemoteName: remoteName}, nil, &d); err != nil {
		return d, err
	}
	if d.Protocol != Protocol {
		return d, fmt.Errorf("Jara protocol mismatch: client %d, remote %d; update both", Protocol, d.Protocol)
	}
	fmt.Printf("Jara %s; protocol %d; storage %s\n", d.Version, d.Protocol, d.Root)
	for _, s := range d.Checks {
		if strings.HasPrefix(s, "SETUP Codex authentication:") {
			command := os.Args[0]
			if strings.ContainsAny(command, " \t\r\n'\"`$;\\&|()<>") {
				command = quote(command)
			}
			s = fmt.Sprintf("SETUP Codex authentication: run %s auth codex --remote %s", command, remoteName)
		}
		if strings.HasPrefix(s, "SETUP Claude Code authentication:") {
			s = fmt.Sprintf("SETUP Claude Code authentication: run claude setup-token locally, then jara auth claude --remote %s", remoteName)
		}
		fmt.Println(s)
	}
	if !d.OK {
		return d, fmt.Errorf("remote needs setup; see diagnostics above")
	}
	return d, nil
}

func codexAuthenticationMissing(d Diagnosis) bool {
	for _, check := range d.Checks {
		if strings.HasPrefix(check, "SETUP Codex authentication:") {
			return true
		}
	}
	return false
}

func forward(r Remote, direction, port, target, bind string) error {
	ports, err := parsePortSpecs([]string{port})
	if err != nil {
		return err
	}
	var forwards, reverses []int
	switch direction {
	case "forward":
		forwards = ports
	case "reverse":
		reverses = ports
	default:
		return fmt.Errorf("unknown tunnel direction %q", direction)
	}
	return openTunnel(r, forwards, reverses, target, target, bind, bind)
}

const maxTunnelPorts = 256

type portSpecs []string

func (p *portSpecs) String() string { return strings.Join(*p, ",") }
func (p *portSpecs) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("port list cannot be empty")
	}
	*p = append(*p, value)
	return nil
}

func parsePortSpecs(specs []string) ([]int, error) {
	var ports []int
	seen := map[int]bool{}
	for _, spec := range specs {
		for _, item := range strings.Split(spec, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				return nil, fmt.Errorf("empty port in %q", spec)
			}
			bounds := strings.Split(item, "-")
			if len(bounds) > 2 {
				return nil, fmt.Errorf("invalid port or range %q", item)
			}
			start, err := tunnelPort(bounds[0])
			if err != nil {
				return nil, err
			}
			end := start
			if len(bounds) == 2 {
				end, err = tunnelPort(bounds[1])
				if err != nil {
					return nil, err
				}
				if end < start {
					return nil, fmt.Errorf("range %q ends before it starts", item)
				}
			}
			if end-start+1 > maxTunnelPorts || len(ports)+end-start+1 > maxTunnelPorts {
				return nil, fmt.Errorf("at most %d tunnel ports are allowed", maxTunnelPorts)
			}
			for port := start; port <= end; port++ {
				if seen[port] {
					return nil, fmt.Errorf("port %d appears more than once", port)
				}
				seen[port] = true
				ports = append(ports, port)
			}
		}
	}
	return ports, nil
}

func tunnelPort(value string) (int, error) {
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q; ports must be 1–65535", value)
	}
	return n, nil
}

func tunnelTarget(target string, port int) (string, error) {
	if target == "" {
		return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), nil
	}
	if strings.ContainsAny(target, "\x00\r\n") {
		return "", fmt.Errorf("invalid destination")
	}
	host, targetPort, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return "", fmt.Errorf("target must be HOST:PORT (bracket IPv6 addresses)")
	}
	if _, err = tunnelPort(targetPort); err != nil {
		return "", fmt.Errorf("target port: %w", err)
	}
	return target, nil
}

func tunnelSpec(bind string, port int, target string) (string, error) {
	if bind == "" || strings.ContainsAny(bind, "\x00\r\n") {
		return "", fmt.Errorf("invalid bind address")
	}
	destination, err := tunnelTarget(target, port)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(strings.Trim(bind, "[]"), strconv.Itoa(port)) + ":" + destination, nil
}

func tunnelArgs(r Remote, forwards, reverses []int, forwardTarget, reverseTarget, localBind, remoteBind string) ([]string, error) {
	if len(forwards) == 0 && len(reverses) == 0 {
		return nil, fmt.Errorf("specify at least one --forward or --reverse port")
	}
	args := append(sshArgs(r, false), "-N", "-o", "ExitOnForwardFailure=yes")
	for _, port := range forwards {
		spec, err := tunnelSpec(localBind, port, forwardTarget)
		if err != nil {
			return nil, fmt.Errorf("forward port %d: %w", port, err)
		}
		args = append(args, "-L", spec)
	}
	for _, port := range reverses {
		spec, err := tunnelSpec(remoteBind, port, reverseTarget)
		if err != nil {
			return nil, fmt.Errorf("reverse port %d: %w", port, err)
		}
		args = append(args, "-R", spec)
	}
	return append(args, "--", r.Host), nil
}

func openTunnel(r Remote, forwards, reverses []int, forwardTarget, reverseTarget, localBind, remoteBind string) error {
	args, err := tunnelArgs(r, forwards, reverses, forwardTarget, reverseTarget, localBind, remoteBind)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Opening SSH tunnel: %d forward(s), %d reverse(s); Ctrl-C closes all.\n", len(forwards), len(reverses))
	return interactive(exec.Command("ssh", args...))
}

func editorAlias(name string, r Remote) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".ssh")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	alias := "jara-" + name
	// Include is managed separately so existing user configuration is preserved.
	generated := filepath.Join(dir, "jara_config")
	c, err := readConfig()
	if err != nil {
		return "", err
	}
	c.Remotes[name] = r
	names := make([]string, 0, len(c.Remotes))
	for n := range c.Remotes {
		names = append(names, n)
	}
	sort.Strings(names)
	var out strings.Builder
	out.WriteString("# Generated by jara edit.\n")
	for _, n := range names {
		v := c.Remotes[n]
		if err = validateRemote(v); err != nil {
			return "", err
		}
		effective, err := exec.Command("ssh", append(sshArgs(v, false), "-G", "--", v.Host)...).Output()
		if err != nil {
			return "", fmt.Errorf("resolve SSH configuration for %s: %w", n, err)
		}
		fmt.Fprintf(&out, "Host jara-%s\n", n)
		allowed := map[string]bool{"hostname": true, "user": true, "port": true, "identityfile": true, "certificatefile": true, "proxyjump": true, "proxycommand": true, "identitiesonly": true, "identityagent": true, "userknownhostsfile": true, "globalknownhostsfile": true, "hostkeyalias": true, "stricthostkeychecking": true, "addressfamily": true}
		for _, line := range strings.Split(string(effective), "\n") {
			key, _, ok := strings.Cut(line, " ")
			if ok && allowed[key] {
				out.WriteString("  " + line + "\n")
			}
		}
		out.WriteString("\n")
	}
	if err = os.WriteFile(generated, []byte(out.String()), 0600); err != nil {
		return "", err
	}
	config := filepath.Join(dir, "config")
	existing, err := os.ReadFile(config)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	include := "Include jara_config"
	if !strings.Contains(string(existing), include) {
		existing = append([]byte(include+"\n"), existing...)
		if err = os.WriteFile(config, existing, 0600); err != nil {
			return "", err
		}
	}
	return alias, nil
}
