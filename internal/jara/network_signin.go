package jara

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"

	"krystof.liskovec/jara/internal/agentrouter"
	"krystof.liskovec/jara/internal/protocol"
)

// After joining, a device can use its hosts at once: they run their agents
// with their own sign-in. Running agents on the device itself needs its own
// CLIs signed in, which offerAgentSignIn takes care of in the same command.

// agentSignIn is what offerAgentSignIn may do; tests replace it.
type agentSignIn struct {
	codexInstalled, claudeInstalled bool
	claudeSignedIn                  func() bool
	codexEverywhere                 func() error       // device code on this device and every host not signed in
	claudeBrowser                   func() error       // claude auth login here
	claudeToken                     func([]byte) error // store a setup-token here
	readToken                       func() ([]byte, error)
}

func realAgentSignIn(c Config) agentSignIn {
	_, codex := installedAgent(protocol.CodexExecutable(), "codex")
	claude, claudeFound := installedAgent(protocol.ClaudeExecutable(), "claude")
	return agentSignIn{
		codexInstalled: codex, claudeInstalled: claudeFound,
		claudeSignedIn:  agentrouter.ClaudeSignedIn,
		codexEverywhere: func() error { return codexAuthEverywhere(c, false, false, false) },
		claudeBrowser:   func() error { return interactive(exec.Command(claude, "auth", "login")) },
		claudeToken:     func(token []byte) error { return agentrouter.StoreClaudeToken(string(token)) },
		readToken:       func() ([]byte, error) { return readClaudeSecret(false, "It is saved on this device only.") },
	}
}

// offerAgentSignIn asks, at the end of jara join, to sign the agent CLIs in.
// Codex signs in on this device and on any host that is not signed in yet,
// each with its own device code; Claude Code signs in here only, so the
// hosts keep the accounts they have.
func offerAgentSignIn(in io.Reader, out io.Writer, a agentSignIn) {
	reader := bufio.NewReader(in)
	ask := func(question, choices string) string {
		fmt.Fprintf(out, "%s %s ", question, choices)
		answer, _ := reader.ReadString('\n')
		return strings.ToLower(strings.TrimSpace(answer))
	}
	if !a.codexInstalled && !a.claudeInstalled {
		fmt.Fprintln(out, "Neither Codex nor Claude Code is installed here: sessions on your hosts work now; install a CLI "+
			"and run jara auth codex --all or jara auth claude to run agents on this device too.")
		return
	}
	fmt.Fprintln(out)
	if a.codexInstalled {
		if answer := ask("Sign Codex in on this device, and on any host that is not signed in yet?", "[Y/n]"); answer == "" || answer == "y" || answer == "yes" {
			if err := a.codexEverywhere(); err != nil {
				fmt.Fprintln(out, "Codex:", err, "(jara auth codex --all tries again)")
			}
		}
	}
	if !a.claudeInstalled {
		return
	}
	if a.claudeSignedIn() {
		fmt.Fprintln(out, "Claude Code is already signed in on this device.")
		return
	}
	switch ask("Sign Claude Code in on this device? In the browser, with a token from claude setup-token, or skip:", "[B/t/s]") {
	case "", "b", "browser":
		if err := a.claudeBrowser(); err != nil {
			fmt.Fprintln(out, "Claude Code:", err, "(claude auth login tries again)")
		}
	case "t", "token":
		token, err := a.readToken()
		if err == nil {
			err = a.claudeToken(token)
		}
		if err != nil {
			fmt.Fprintln(out, "Claude Code:", err, "(jara auth claude --all tries again)")
			return
		}
		fmt.Fprintln(out, "Saved the token; Claude Code on this device uses it from its next turn.")
	}
}

// signInAfterJoin runs the offer when someone is at a terminal; otherwise it
// says how to sign in later.
func signInAfterJoin(skip bool) {
	if skip || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		fmt.Println("To run agents on this device too: jara auth codex --all, and claude auth login.")
		return
	}
	c, err := readConfig()
	if err != nil {
		fmt.Println("Could not read the configuration to offer signing in:", err)
		return
	}
	offerAgentSignIn(os.Stdin, os.Stdout, realAgentSignIn(c))
}
