package jag

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
)

func authCommand(c Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("use jag auth codex|gitlab|gitea [--remote NAME] [--host HOST] [--url URL] --token-stdin")
	}
	provider := args[0]
	f := flag.NewFlagSet("auth", flag.ContinueOnError)
	name := f.String("remote", c.Default, "remote")
	host := f.String("host", "gitlab.com", "GitLab hostname")
	server := f.String("url", "", "Gitea HTTPS URL")
	stdin := f.Bool("token-stdin", false, "read token from standard input")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected auth argument")
	}
	r, ok := c.Remotes[*name]
	if !ok {
		return fmt.Errorf("unknown remote %q", *name)
	}
	if provider == "codex" {
		return interactive(sshCommand(r, true, shellArgs("codex", "login", "--device-auth")))
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
	return interactive(sshCommand(r, false, shellArgs("jag", "_auth", provider, destination)))
}
func validateProvider(provider, destination string) error {
	switch provider {
	case "gitlab":
		if destination == "" || strings.ContainsAny(destination, "/ \r\n\t") || strings.HasPrefix(destination, "-") {
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
func remoteAuth(provider, destination string) error {
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
	fingerprint := sha256.Sum256(append([]byte(provider+"\n"+destination+"\n"), secret...))
	marker := filepath.Join(dataRoot(), "auth", provider+".sha256")
	if old, e := os.ReadFile(marker); e == nil && string(old) == hex.EncodeToString(fingerprint[:]) {
		fmt.Fprintln(os.Stderr, provider+" credentials already configured")
		return nil
	}
	var cmd *exec.Cmd
	if provider == "gitlab" {
		cmd = exec.Command("glab", "auth", "login", "--hostname", destination, "--stdin", "--git-protocol", "https", "--setup-git-credential-helper")
		cmd.Stdin = bytes.NewReader(secret)
	} else {
		cmd = exec.Command("tea", "login", "add", "--name", "jag", "--url", destination, "--git-credentials")
		cmd.Env = append(os.Environ(), "GITEA_SERVER_TOKEN="+string(secret))
	}
	// Provider output is suppressed here so credentials cannot enter startup logs.
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("%s authentication failed; verify installed CLI, host and token permissions (for Tea token rotation, remove its managed 'jag' login first)", provider)
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
