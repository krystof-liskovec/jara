package jara

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
	"golang.org/x/term"
)

// A long-lived Claude token (claude setup-token) or an Anthropic API key can
// serve every machine at once: jara auth claude --all and /login claude token
// everywhere save it on this device and send it to every remote. A browser
// sign-in is not shared, as Claude Code renews it and copies would conflict.

// readClaudeSecret reads a token from standard input or a hidden prompt.
func readClaudeSecret(tokenStdin bool, where string) ([]byte, error) {
	var secret []byte
	var err error
	if tokenStdin {
		secret, err = io.ReadAll(io.LimitReader(os.Stdin, 65537))
	} else {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return nil, fmt.Errorf("no terminal for a hidden prompt; pipe the token with --token-stdin")
		}
		fmt.Fprintln(os.Stderr, "Generate a long-lived Claude token by running `claude setup-token` on any machine with a browser.")
		fmt.Fprintln(os.Stderr, "An Anthropic API key (sk-ant-api…) is also accepted. "+where)
		fmt.Fprint(os.Stderr, "Claude token (input hidden): ")
		secret, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		return nil, err
	}
	secret = bytes.TrimSpace(secret)
	if err = validateClaudeToken(string(secret)); err != nil {
		return nil, err
	}
	return secret, nil
}

// readSecretLine reads one secret from standard input or a hidden prompt.
func readSecretLine(fromStdin bool, prompt string) (string, error) {
	if fromStdin {
		data, err := io.ReadAll(io.LimitReader(os.Stdin, 65537))
		return strings.TrimSpace(string(data)), err
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("no terminal for a hidden prompt; pipe the key with --token-stdin")
	}
	fmt.Fprint(os.Stderr, prompt)
	data, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return strings.TrimSpace(string(data)), err
}

// sendClaudeToken stores secret in a remote's agent home over SSH standard input.
func sendClaudeToken(remote Remote, secret []byte) error {
	cmd := sshCommand(remote, false, shellArgs("jara", "_auth", protocol.AgentClaude, claudeTokenDestination))
	cmd.Stdin = bytes.NewReader(secret)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Run(); err != nil {
		if detail := firstLine(output.String()); detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

// shareClaudeToken saves secret here and on every remote at once, and tells
// how each went.
func shareClaudeToken(c Config, secret []byte) string {
	if err := validateClaudeToken(string(secret)); err != nil {
		return "Error: " + err.Error() + "\n"
	}
	places := chatRemotePlaces(c)
	results := make([]string, len(places))
	var wait sync.WaitGroup
	for i, place := range places {
		wait.Go(func() {
			if place == "" {
				results[i] = "This device: " + savedOrNot(agentrouter.StoreClaudeToken(string(secret)))
				return
			}
			results[i] = place + ": " + savedOrNot(sendClaudeToken(c.Remotes[place], secret))
		})
	}
	wait.Wait()
	text := strings.Join(results, "\n") + "\n"
	if strings.Contains(text, "not saved") {
		text += "Run it again to reach the hosts that were not saved.\n"
	}
	text += "Claude Code uses the token from its next turn"
	if source := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") + os.Getenv("ANTHROPIC_API_KEY"); source != "" {
		text += ", except where CLAUDE_CODE_OAUTH_TOKEN or ANTHROPIC_API_KEY is set, as on this device"
	}
	return text + ".\n"
}

func savedOrNot(err error) string {
	if err != nil {
		return "not saved (" + err.Error() + ")"
	}
	return "saved"
}
