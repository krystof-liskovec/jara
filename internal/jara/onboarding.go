package jara

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

func guidedSetup(c Config) error {
	reader := bufio.NewReader(os.Stdin)
	name, err := askSetup(reader, "Remote name", "work")
	if err != nil {
		return err
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("remote name may contain only letters, digits, underscores and hyphens")
	}
	var r Remote
	var diagnosis Diagnosis
	checkRemote := false
	if saved, ok := c.Remotes[name]; ok {
		r = saved
		fmt.Printf("Checking SSH access and Jara on %s...\n", r.Host)
		if diagnosis, err = diagnoseRemote(name, r); err != nil {
			return fmt.Errorf("saved Jara remote %q is unavailable: %w; choose a new name to deploy another host", name, err)
		}
	} else {
		if _, managed := c.HostDeployments[name]; managed {
			return fmt.Errorf("guided deployment %q has no saved remote connection; remove its local entry or repair the Jara config", name)
		}
		method, err := askSetup(reader, "Connect to an existing Jara host or deploy a new one? [connect/deploy]", "connect")
		if err != nil {
			return err
		}
		switch strings.ToLower(method) {
		case "connect":
			r, err = connectExistingJaraHost(reader)
			if err != nil {
				return err
			}
			fmt.Printf("Checking SSH access and Jara on %s...\n", r.Host)
			if diagnosis, err = diagnoseRemote(name, r); err != nil {
				return fmt.Errorf("could not connect to the existing Jara host: %w", err)
			}
		case "deploy":
			r, err = provisionDockerHost(&c, name, reader)
			if err != nil {
				return err
			}
			checkRemote = true
		default:
			return fmt.Errorf("choose connect or deploy")
		}
	}
	c.Remotes[name] = r
	c.Default = name
	if err = saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("Saved %q as the default remote.\n", name)
	if checkRemote {
		fmt.Println("Waiting for the Jara SSH service...")
		if err = waitForJaraSSH(r, 90*time.Second); err != nil {
			return fmt.Errorf("deployment was saved, but Jara SSH did not become ready: %w", err)
		}
		fmt.Println("Checking the new Jara deployment...")
		if diagnosis, err = diagnoseRemote(name, r); err != nil {
			return err
		}
	}
	if _, managed := c.HostDeployments[name]; !managed {
		c, err = readConfig()
		if err != nil {
			return err
		}
		if err = offerDeploymentConnection(&c, name, r, reader); err != nil {
			return fmt.Errorf("Jara connection was saved, but Docker host registration failed: %w; rerun jara setup to retry", err)
		}
	}
	if err = setupHostCodexArgs(r, name, reader); err != nil {
		return err
	}

	if codexAuthenticationMissing(diagnosis) {
		fmt.Println("Codex sign-in is saved in the remote Jara environment's persistent home.")
		yes, askErr := askYes(reader, "Sign in to Codex on the remote now?", true)
		if askErr != nil {
			return askErr
		}
		if yes {
			if err = authCommand(c, []string{"codex", "--remote", name}); err != nil {
				return err
			}
		} else {
			fmt.Printf("You can sign in later with `jara auth codex --remote %s`.\n", name)
		}
	} else {
		fmt.Println("Codex is already signed in on this Jara host.")
	}
	if err = offerClaudeSignIn(c, name, diagnosis, reader); err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	_, _, isGit := workspaceRoot(cwd, false)
	workspaceChoices := "copy/skip"
	if isGit {
		workspaceChoices = "open/copy/skip"
	}
	mode, err := askSetup(reader, "Open this folder now? ["+workspaceChoices+"]", "skip")
	if err != nil {
		return err
	}
	switch strings.ToLower(mode) {
	case "open", "clone":
		if !isGit {
			return fmt.Errorf("clone is available from inside a Git repository; choose copy for this folder")
		}
		configureGit, e := askYes(reader, "Set up one Git credential for all repositories on this Git server?", false)
		if e != nil {
			return e
		}
		if configureGit {
			if err = authCommand(c, []string{"git", "--remote", name}); err != nil {
				return err
			}
		}
		return Run([]string{"--remote", name})
	case "copy":
		return Run([]string{"--remote", name, "--copy"})
	case "skip", "":
		fmt.Printf("Ready. Run jara --remote %s in a Git repository, or --copy in a folder.\n", name)
		return nil
	default:
		return fmt.Errorf("choose open, copy, or skip")
	}
}

func setupHostCodexArgs(remote Remote, name string, reader *bufio.Reader) error {
	configure, err := askYes(reader, "Set default Codex arguments for this host?", false)
	if err != nil || !configure {
		return err
	}
	fmt.Fprintln(os.Stderr, "Enter one argument per prompt; leave it blank when finished.")
	var args []string
	for {
		arg, e := askSetup(reader, "Codex argument", "")
		if e != nil {
			return e
		}
		if arg == "" {
			break
		}
		args = append(args, arg)
		if e = validateCodexArgs(args); e != nil {
			return e
		}
	}
	if len(args) == 0 {
		err = clearRemoteCodexArgs(remote)
	} else {
		err = setRemoteCodexArgs(remote, args)
	}
	if err != nil {
		return fmt.Errorf("configure Codex arguments on %s; update Jara on that host if needed: %w", name, err)
	}
	if len(args) == 0 {
		fmt.Printf("Using deployment Codex argument defaults on %s.\n", name)
		return nil
	}
	fmt.Printf("Saved %d default Codex argument(s) on %s.\n", len(args), name)
	return nil
}

func waitForJaraSSH(r Remote, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastOutput string
	for {
		cmd := sshCommand(r, false, "true")
		cmd.Stdin = os.Stdin
		cmd.Stdout = io.Discard
		var stderr bytes.Buffer
		cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
		lastErr = cmd.Run()
		if lastErr == nil {
			return nil
		}
		lastOutput = strings.TrimSpace(stderr.String())
		if !sshStartupError(lastOutput) {
			return fmt.Errorf("SSH readiness check failed: %w: %s", lastErr, lastOutput)
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out after %s: %w: %s", timeout, lastErr, lastOutput)
		}
		time.Sleep(time.Second)
	}
}

func sshStartupError(output string) bool {
	output = strings.ToLower(output)
	for _, marker := range []string{
		"connection refused", "connection timed out", "operation timed out",
		"no route to host", "connection reset by peer", "connection closed by remote host",
		"kex_exchange_identification", "banner exchange",
	} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}

func askSetup(reader *bufio.Reader, prompt, defaultValue string) (string, error) {
	return askSetupPrompt(reader, prompt, defaultValue, false)
}

func askSetupPath(reader *bufio.Reader, prompt, defaultValue string) (string, error) {
	return askSetupPrompt(reader, prompt, defaultValue, true)
}

func askSetupPrompt(reader *bufio.Reader, prompt, defaultValue string, completePaths bool) (string, error) {
	if completePaths && term.IsTerminal(int(os.Stdin.Fd())) {
		prompt += " (Tab completes paths)"
	}
	label := prompt + ": "
	if defaultValue != "" {
		label = fmt.Sprintf("%s [%s]: ", prompt, defaultValue)
	}
	fmt.Fprint(os.Stderr, label)
	value := ""
	var err error
	if completePaths {
		var handled bool
		value, handled, err = readSetupPathLine(reader, int(os.Stdin.Fd()), os.Stderr, label)
		if handled {
			if err != nil {
				return "", err
			}
		} else {
			value, err = reader.ReadString('\n')
		}
	} else {
		value, err = reader.ReadString('\n')
	}
	if err != nil && value == "" {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func askYes(reader *bufio.Reader, prompt string, defaultYes bool) (bool, error) {
	choice := "Y/n"
	if !defaultYes {
		choice = "y/N"
	}
	answer, err := askSetup(reader, prompt+" ["+choice+"]", "")
	if err != nil {
		return false, err
	}
	if answer == "" {
		return defaultYes, nil
	}
	switch strings.ToLower(answer) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, fmt.Errorf("answer yes or no")
	}
}
