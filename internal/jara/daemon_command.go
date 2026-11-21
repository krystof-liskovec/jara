package jara

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	daemonUnitName    = "jara.service"
	daemonLaunchLabel = "jara.daemon"
	daemonUsage       = "use jara daemon install [--no-linger] | uninstall | status | run"
)

// liveCommand runs the commands about running sessions and the network of
// devices: jara ps, attach, daemon, device, network and join.
func liveCommand(c Config, command string, args []string) error {
	switch command {
	case "ps":
		return psCommand(c, args)
	case "attach":
		return attachCommand(c, args)
	case "device":
		return deviceCommand(c, args)
	case "network":
		return networkCommand(c, args)
	case "join":
		return joinCommand(c, args)
	}
	return daemonCommand(args)
}

// daemonCommand is `jara daemon`: the per-user service that keeps this
// device's agent sessions running in the background.
func daemonCommand(args []string) error {
	if len(args) == 0 {
		return errors.New(daemonUsage)
	}
	switch args[0] {
	case "run":
		if len(args) != 1 {
			return errors.New(daemonUsage)
		}
		return runDaemonSupervisor()
	case "install":
		noLinger := len(args) == 2 && args[1] == "--no-linger"
		if len(args) > 2 || len(args) == 2 && !noLinger {
			return errors.New(daemonUsage)
		}
		return installDaemon(!noLinger)
	case "uninstall":
		if len(args) != 1 {
			return errors.New(daemonUsage)
		}
		return uninstallDaemon()
	case "status":
		if len(args) != 1 {
			return errors.New(daemonUsage)
		}
		return daemonStatus()
	}
	return errors.New(daemonUsage)
}

// daemonServiceFile is where the service definition is installed.
func daemonServiceFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "linux":
		config := os.Getenv("XDG_CONFIG_HOME")
		if config == "" {
			config = filepath.Join(home, ".config")
		}
		return filepath.Join(config, "systemd", "user", daemonUnitName), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", daemonLaunchLabel+".plist"), nil
	}
	return "", fmt.Errorf("the Jara daemon requires Linux (systemd) or macOS")
}

// daemonSystemdUnit stops only the daemon process first (KillMode=mixed):
// it then stops each session itself, so running work is handed over or
// recorded before anything is killed.
func daemonSystemdUnit(executable string) string {
	return fmt.Sprintf(`[Unit]
Description=Jara agent sessions

[Service]
Type=simple
ExecStart=%s daemon run
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=40

[Install]
WantedBy=default.target
`, systemdQuote(executable))
}

func daemonLaunchAgent(executable, logPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array><string>%s</string><string>daemon</string><string>run</string></array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
	<key>ExitTimeOut</key><integer>40</integer>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonLaunchLabel, xmlText(executable), xmlText(logPath))
}

func systemdQuote(path string) string {
	if !strings.ContainsAny(path, " \t\"'\\") {
		return path
	}
	return strconv.Quote(path)
}

func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func installDaemon(linger bool) error {
	file, err := daemonServiceFile()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, e := filepath.EvalSymlinks(executable); e == nil {
		executable = resolved
	}
	if err = os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		if err = os.MkdirAll(daemonDir(), 0700); err != nil {
			return err
		}
		if err = os.WriteFile(file, []byte(daemonLaunchAgent(executable, filepath.Join(daemonDir(), "daemon.log"))), 0644); err != nil {
			return err
		}
		domain := "gui/" + strconv.Itoa(os.Getuid())
		_ = exec.Command("launchctl", "bootout", domain+"/"+daemonLaunchLabel).Run()
		if err = runServiceTool("launchctl", "bootstrap", domain, file); err != nil {
			return err
		}
		fmt.Println("The Jara daemon runs now and starts when you log in.")
		return nil
	}
	if err = os.WriteFile(file, []byte(daemonSystemdUnit(executable)), 0644); err != nil {
		return err
	}
	if err = runServiceTool("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	if err = runServiceTool("systemctl", "--user", "enable", "--now", daemonUnitName); err != nil {
		return err
	}
	fmt.Println("The Jara daemon runs now; sessions you open start under it.")
	// A user service runs inside the user's service manager, which logind
	// starts at the first login and stops after the last logout unless the
	// user lingers. Lingering makes it a real background service: running
	// from boot, without a login, after logging out.
	state := lingering()
	if state == "no" && linger {
		if err = runServiceTool("loginctl", "enable-linger"); err != nil {
			fmt.Println("Could not turn on lingering, so the daemon starts at your next login, not at boot, "+
				"and stops when you log out of every session:", err)
			return nil
		}
		state = "yes"
	}
	switch state {
	case "yes":
		fmt.Println("It starts at boot, without a login, and keeps running after you log out.")
	case "no":
		fmt.Println("It starts at your next login, not at boot, and stops when you log out of every session (--no-linger).")
	}
	return nil
}

func uninstallDaemon() error {
	file, err := daemonServiceFile()
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+daemonLaunchLabel).Run()
	} else {
		_ = exec.Command("systemctl", "--user", "disable", "--now", daemonUnitName).Run()
	}
	if err = os.Remove(file); err != nil && !os.IsNotExist(err) {
		return err
	}
	if runtime.GOOS == "linux" {
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	fmt.Println("Removed the Jara daemon. Running sessions it started have handed their work over or stopped; jara starts new ones directly.")
	return nil
}

func daemonStatus() error {
	if answer, err := callDaemon(daemonMessage{Op: "ping"}); err == nil {
		fmt.Printf("The Jara daemon %s is running (pid %d) with %d session(s) it started.\n", answer.Version, answer.PID, answer.Sessions)
	} else {
		fmt.Println("The Jara daemon is not running; sessions start from your terminal instead.")
	}
	if file, err := daemonServiceFile(); err == nil {
		if _, statErr := os.Stat(file); statErr == nil {
			fmt.Println("Installed as", file+".")
		} else {
			fmt.Println("Not installed; jara daemon install sets it up.")
		}
	}
	if runtime.GOOS == "linux" {
		switch lingering() {
		case "yes":
			fmt.Println("Lingering is on: it runs without a login and starts at boot.")
		case "no":
			fmt.Println("Lingering is off: it stops when you log out of every session.")
		}
	}
	if pending := pendingDaemonResumes(); len(pending) > 0 {
		fmt.Printf("%d interrupted session(s) wait to continue: %s.\n", len(pending), strings.Join(pending, ", "))
	}
	return nil
}

// lingering reports systemd-logind's Linger setting for this user: yes, no,
// or "" when it cannot tell.
func lingering() string {
	account, err := user.Current()
	if err != nil {
		return ""
	}
	out, err := exec.Command("loginctl", "show-user", account.Username, "--property=Linger", "--value").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func runServiceTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
