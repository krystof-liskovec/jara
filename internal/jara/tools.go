package jara

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func prepareTools(dir string) error {
	return prepareToolsWithOutput(dir, true)
}

func prepareToolsQuiet(dir string) error {
	return prepareToolsWithOutput(dir, false)
}

func prepareToolsWithOutput(dir string, showOutput bool) error {
	if _, err := exec.LookPath("mise"); err != nil {
		return fmt.Errorf("project toolchains require mise; rebuild the environment from the current Jara source")
	}
	cmd := exec.Command("mise", "install")
	cmd.Dir = dir
	if showOutput {
		if err := interactive(cmd); err != nil {
			return fmt.Errorf("project tool installation failed in %s; inspect the mise configuration, version requests, trust settings and download connectivity: %w", dir, err)
		}
		return nil
	}
	cmd.Stdin = os.Stdin
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if detail != "" {
			return fmt.Errorf("project tool installation failed in %s; inspect the mise configuration, version requests, trust settings and download connectivity: %w\n%s", dir, err, detail)
		}
		return fmt.Errorf("project tool installation failed in %s; inspect the mise configuration, version requests, trust settings and download connectivity: %w", dir, err)
	}
	return nil
}

func projectToolCommand(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("mise", append([]string{"exec", "--"}, args...)...)
	cmd.Dir = dir
	return cmd
}

func toolsCommand(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("invalid internal tools invocation")
	}
	action, dir := args[0], args[1]
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("project directory is missing: %s", dir)
	}
	switch action {
	case "install":
		return prepareTools(dir)
	case "current":
		cmd := exec.Command("mise", "current")
		cmd.Dir = dir
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err = cmd.Run(); err != nil {
			return err
		}
		if out.Len() == 0 {
			fmt.Println("No project tool versions configured; using available image defaults.")
		} else {
			fmt.Print(out.String())
		}
		return nil
	case "exec":
		if len(args) < 3 {
			return fmt.Errorf("tool command required")
		}
		return interactive(projectToolCommand(dir, args[2:]...))
	default:
		return fmt.Errorf("unknown tools action")
	}
}
