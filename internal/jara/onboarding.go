package jara

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
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
	checkRemote := false
	if saved, ok := c.Remotes[name]; ok {
		r = saved
		fmt.Printf("Checking SSH access and Jara on %s...\n", r.Host)
		if err := doctor(name, r); err != nil {
			return fmt.Errorf("saved Jara remote %q is unavailable: %w; choose a new name to deploy another host", name, err)
		}
	} else {
		if _, managed := c.HostDeployments[name]; managed {
			return fmt.Errorf("guided deployment %q has no saved remote connection; remove its local entry or repair the Jara config", name)
		}
		var err error
		r, err = provisionDockerHost(&c, name, reader)
		if err != nil {
			return err
		}
		checkRemote = true
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
		if err = doctor(name, r); err != nil {
			return err
		}
	}

	fmt.Println("Codex sign-in is saved in the remote Jara environment's persistent home.")
	yes, err := askYes(reader, "Sign in to Codex on the remote now?", true)
	if err != nil {
		return err
	}
	if yes {
		if err = authCommand(c, []string{"codex", "--remote", name}); err != nil {
			return err
		}
	} else {
		fmt.Printf("You can sign in later with `jara auth codex --remote %s`.\n", name)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	_, _, isGit := workspaceRoot(cwd, false)
	workspaceChoices := "copy/skip"
	if isGit {
		workspaceChoices = "clone/copy/skip"
	}
	mode, err := askSetup(reader, "Open this folder now? ["+workspaceChoices+"]", "skip")
	if err != nil {
		return err
	}
	switch strings.ToLower(mode) {
	case "clone":
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
		return Run([]string{"--remote", name, "--clone"})
	case "copy":
		return Run([]string{"--remote", name, "--copy"})
	case "skip", "":
		fmt.Printf("Ready. Run jara --remote %s --clone in a Git repository, or --copy in a folder.\n", name)
		return nil
	default:
		return fmt.Errorf("choose clone, copy, or skip")
	}
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
	if defaultValue != "" {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", prompt, defaultValue)
	} else {
		fmt.Fprintf(os.Stderr, "%s: ", prompt)
	}
	value, err := reader.ReadString('\n')
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
