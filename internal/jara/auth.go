package jara

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

func authCommand(c Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use jara auth git|codex|claude|gitlab|gitea [options]")
	}
	provider := args[0]
	f := flag.NewFlagSet("auth", flag.ContinueOnError)
	name := f.String("remote", c.Default, "remote")
	host := f.String("host", "gitlab.com", "GitLab hostname")
	server := f.String("url", "", "Gitea HTTPS URL")
	username := f.String("username", "", "Git username for --token-stdin")
	stdin := f.Bool("token-stdin", false, "read token from standard input")
	all := f.Bool("all", false, "sign in on this device and every remote")
	apiKey := f.Bool("api-key", false, "codex: an OpenAI API key instead of a ChatGPT account")
	add := f.Bool("add", false, "codex --all: add a further account on every host, keeping the others")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected auth argument")
	}
	if *all && provider == "codex" {
		return codexAuthEverywhere(c, *apiKey, *stdin, *add)
	}
	if *all {
		if provider != protocol.AgentClaude {
			return fmt.Errorf("--all works with jara auth codex and jara auth claude")
		}
		secret, err := readClaudeSecret(*stdin, "It is saved on this device and every remote.")
		if err != nil {
			return err
		}
		report := shareClaudeToken(c, secret)
		fmt.Print(report)
		if strings.Contains(report, "not saved") {
			return fmt.Errorf("some hosts were not saved")
		}
		return nil
	}
	r, ok := c.Remotes[*name]
	if !ok {
		return fmt.Errorf("unknown remote %q", *name)
	}
	if provider == "codex" {
		return interactive(sshCommand(r, true, shellArgs("codex", "login", "--device-auth")))
	}
	if provider == "git" {
		return gitAuthCommand(r, *username, *stdin)
	}
	if provider == protocol.AgentClaude {
		return claudeAuthCommand(r, *stdin)
	}
	if !*stdin {
		return fmt.Errorf("supply --token-stdin and pipe a token from secure storage")
	}
	destination := *host
	if provider == "gitea" {
		destination = *server
	}
	if err := validateProvider(provider, destination); err != nil {
		return err
	}
	return interactive(sshCommand(r, false, shellArgs("jara", "_auth", provider, destination)))
}
func validateProvider(provider, destination string) error {
	switch provider {
	case protocol.AgentClaude:
		if destination != claudeTokenDestination {
			return fmt.Errorf("Claude tokens use destination %q", claudeTokenDestination)
		}
	case "git":
		_, err := parseGitCredentialTarget(destination)
		return err
	case "gitlab":
		parsed, err := url.Parse("https://" + destination)
		if destination == "" || strings.HasPrefix(destination, "-") || strings.ContainsAny(destination, "/\\@?#% \r\n\t") || err != nil || parsed.Host != destination || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return fmt.Errorf("GitLab requires a hostname, optionally with port")
		}
	case "gitea":
		u, err := url.Parse(destination)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("Gitea requires an HTTPS URL without credentials, query or fragment")
		}
	default:
		return fmt.Errorf("unknown auth provider")
	}
	return nil
}
func remoteAuth(provider, destination string, usernames ...string) error {
	if err := validateProvider(provider, destination); err != nil {
		return err
	}
	secret, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
	if err != nil {
		return err
	}
	secret = bytes.TrimSpace(secret)
	if len(secret) == 0 || len(secret) > 65536 || bytes.ContainsAny(secret, "\r\n\x00") {
		return fmt.Errorf("expected a nonempty single-line token of at most 64 KiB")
	}
	if provider == protocol.AgentClaude {
		return storeClaudeToken(secret)
	}
	if provider == "git" {
		username := ""
		if len(usernames) > 0 {
			username = usernames[0]
		}
		if username == "" || strings.ContainsAny(username, "\r\n\x00") {
			return fmt.Errorf("Git credential username is required")
		}
		return storeGitCredential(destination, username, string(secret))
	}
	fingerprint := sha256.Sum256(append([]byte(provider+"\n"+destination+"\n"), secret...))
	marker := filepath.Join(fsutil.DataRoot(), "auth", provider+".sha256")
	if old, e := os.ReadFile(marker); e == nil && string(old) == hex.EncodeToString(fingerprint[:]) {
		if err := saveProviderCredential(provider, destination, string(secret)); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, provider+" credentials already configured")
		return nil
	}
	var cmd *exec.Cmd
	if provider == "gitlab" {
		cmd = exec.Command("glab", "auth", "login", "--hostname", destination, "--stdin", "--git-protocol", "https", "--setup-git-credential-helper")
		cmd.Stdin = bytes.NewReader(secret)
	} else {
		cmd = exec.Command("tea", "login", "add", "--name", "jara", "--url", destination, "--git-credentials")
		cmd.Env = append(os.Environ(), "GITEA_SERVER_TOKEN="+string(secret))
	}
	// Provider output is suppressed here so credentials cannot enter startup logs.
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("%s authentication failed; verify installed CLI, host and token permissions (for Tea token rotation, remove its managed 'jara' login first)", provider)
	}
	if err = saveProviderCredential(provider, destination, string(secret)); err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(marker, []byte(hex.EncodeToString(fingerprint[:])), 0600); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, provider+" credentials configured for "+destination)
	return nil
}

func gitAuthCommand(remote Remote, username string, tokenStdin bool) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, _, isGit := workspaceRoot(cwd, false)
	if !isGit {
		return fmt.Errorf("run `jara auth git` from a Git repository with an origin remote")
	}
	origin, err := gitOutput(root, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("repository needs an origin remote: %w", err)
	}
	target, err := parseGitCredentialTarget(origin)
	if err != nil {
		return err
	}
	var secret []byte
	if tokenStdin {
		if username == "" {
			return fmt.Errorf("pass --username with --token-stdin")
		}
		secret, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
		if err != nil {
			return err
		}
		secret = bytes.TrimSpace(secret)
	} else {
		credential, found := localGitCredential(target)
		if found {
			if username == "" {
				username = credential.Username
			}
			secret = []byte(credential.Password)
			fmt.Fprintf(os.Stderr, "Using this checkout's local Git credential for %s; it will be shared by repositories on that instance.\n", target.Host)
		} else {
			if username == "" {
				username, err = promptGitUsername()
				if err != nil {
					return err
				}
			}
			secret, err = promptGitToken(target.Host)
			if err != nil {
				return err
			}
		}
	}
	if len(secret) == 0 || len(secret) > 65536 || bytes.ContainsAny(secret, "\r\n\x00") {
		return fmt.Errorf("expected a nonempty single-line Git token of at most 64 KiB")
	}
	if username == "" || strings.ContainsAny(username, "\r\n\x00") {
		return fmt.Errorf("Git username is required")
	}
	cmd := sshCommand(remote, false, shellArgs("jara", "_auth", "git", target.URL, username))
	cmd.Stdin = bytes.NewReader(secret)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("configure Git credentials on remote: %w", err)
	}
	return nil
}

func localGitCredential(target gitCredentialTarget) (gitCredential, bool) {
	dir, err := os.MkdirTemp("", "jara-git-credential-")
	if err != nil {
		return gitCredential{}, false
	}
	defer os.RemoveAll(dir)
	cmd := exec.Command("git", "credential", "fill")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(credentialInput(target, "", ""))
	output, err := cmd.Output()
	if err != nil {
		return gitCredential{}, false
	}
	values := parseCredentialFields(string(output))
	credential := gitCredential{Username: values["username"], Password: values["password"]}
	return credential, credential.Username != "" && credential.Password != ""
}

func promptGitUsername() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("Git credentials are unavailable locally; pass --username and --token-stdin")
	}
	fmt.Fprint(os.Stderr, "Git username: ")
	var username string
	if _, err := fmt.Fscanln(os.Stdin, &username); err != nil {
		return "", err
	}
	return username, nil
}

func promptGitToken(host string) ([]byte, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, fmt.Errorf("Git credentials are unavailable locally; pass --username and --token-stdin")
	}
	fmt.Fprintf(os.Stderr, "No reusable HTTPS Git credential was found for %s. Create an access token in your Git server account settings with repository read/write permission.\n", host)
	fmt.Fprintln(os.Stderr, "For Gitea, open Settings > Applications and select write:repository. This credential will be shared across repositories on this instance, and the remote agent can use it to push.")
	fmt.Fprint(os.Stderr, "Git token (input hidden): ")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return secret, err
}
