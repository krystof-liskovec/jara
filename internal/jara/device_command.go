package jara

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const deviceUsage = "use jara device allow | deny | status | list [--remote NAME] | open DEVICE [FOLDER] [--remote NAME] [--new]"

// deviceCommand is `jara device`: whether other devices may start and join
// agent sessions on this one, and reaching the devices that allow it.
func deviceCommand(c Config, args []string) error {
	if len(args) == 0 {
		return errors.New(deviceUsage)
	}
	switch args[0] {
	case "allow", "deny":
		if len(args) != 1 {
			return errors.New(deviceUsage)
		}
		return setDeviceCallable(c, args[0] == "allow")
	case "status":
		return deviceStatus(c)
	case "list":
		f := flag.NewFlagSet("device list", flag.ContinueOnError)
		remote := f.String("remote", "", "only devices linked to this remote host")
		if err := f.Parse(args[1:]); err != nil || f.NArg() != 0 {
			return errors.New(deviceUsage)
		}
		devices, problems := collectDevices(c, *remote)
		printDevices(devices, problems)
		return nil
	case "open":
		return deviceOpenCommand(c, args[1:])
	}
	return errors.New(deviceUsage)
}

func setDeviceCallable(c Config, allow bool) error {
	c.DeviceCallable = allow
	if err := saveConfig(c); err != nil {
		return err
	}
	if !allow {
		fmt.Println("Other devices can no longer start or join sessions here; the Jara daemon drops its links within a minute.")
		return nil
	}
	saveDeviceEnvironment()
	fmt.Printf("Other devices can now start and join agent sessions on %s, in every folder you have opened with Jara, "+
		"through your remote hosts. Sessions they start use this shell's environment.\n", runnerDeviceName())
	fmt.Println("Anyone who can log in to one of your remote hosts can then run agents here as you: keep the hosts " +
		"off the public internet, for example reachable only over Tailscale.")
	if _, err := callDaemon(daemonMessage{Op: "ping"}); err != nil {
		fmt.Println("The Jara daemon is not running, so nothing links this device yet; start it with jara daemon install.")
	}
	return nil
}

func deviceStatus(c Config) error {
	name := runnerDeviceName()
	if !c.DeviceCallable {
		fmt.Printf("%s does not accept sessions from other devices (jara device allow).\n", name)
	} else {
		fmt.Printf("%s accepts sessions from other devices through: %s.\n", name, strings.Join(c.rankedRemotes(), ", "))
	}
	if _, err := callDaemon(daemonMessage{Op: "ping"}); err != nil {
		fmt.Println("The Jara daemon is not running; this device is linked only while it runs.")
	}
	folders := deviceFolders(c)
	fmt.Printf("%d folder(s) opened with Jara here.\n", len(folders))
	return nil
}

// reachableDevice is a device and the remote host that reaches it.
type reachableDevice struct {
	deviceInfo
	Remote string
}

// collectDevices asks each remote host (or just one) for its linked
// devices; a device linked to several hosts is listed with the first.
func collectDevices(c Config, onlyRemote string) ([]reachableDevice, []string) {
	names := c.rankedRemotes()
	if onlyRemote != "" {
		names = []string{onlyRemote}
	}
	answers := make([][]deviceInfo, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		remote, ok := c.Remotes[name]
		if !ok {
			errs[i] = fmt.Errorf("not configured")
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = rpc(remote, Request{Op: "device-list"}, nil, &answers[i])
		}()
	}
	wg.Wait()
	var devices []reachableDevice
	var problems []string
	seen := map[string]bool{}
	for i, name := range names {
		if errs[i] != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, errs[i]))
			continue
		}
		for _, device := range answers[i] {
			if !seen[device.Name] {
				seen[device.Name] = true
				devices = append(devices, reachableDevice{deviceInfo: device, Remote: name})
			}
		}
	}
	return devices, problems
}

func printDevices(devices []reachableDevice, problems []string) {
	if len(devices) == 0 {
		fmt.Println("No devices accept sessions right now (jara device allow on a device with the Jara daemon running).")
	}
	for _, device := range devices {
		fmt.Printf("%s (via %s)\n", device.Name, device.Remote)
		if device.Error != "" {
			fmt.Println("  " + device.Error)
		}
		for i, folder := range device.Folders {
			state := ""
			if folder.Running {
				state = "  running"
				if folder.Busy {
					state = "  working"
				}
			}
			fmt.Printf("  %d  %s%s\n", i+1, folder.Folder, state)
		}
	}
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, "Could not list devices on", problem)
	}
}

// deviceOpenCommand is `jara device open DEVICE [FOLDER]`: start or join the
// session of a folder on another device. FOLDER is its path there, its name,
// or its number in jara device list; without it, Jara asks.
func deviceOpenCommand(c Config, args []string) error {
	var positional []string
	f := flag.NewFlagSet("device open", flag.ContinueOnError)
	remoteName := f.String("remote", "", "the remote host to reach the device through")
	fresh := f.Bool("new", false, "start a new conversation")
	for {
		if err := f.Parse(args); err != nil {
			return err
		}
		if f.NArg() == 0 {
			break
		}
		positional, args = append(positional, f.Arg(0)), f.Args()[1:]
	}
	if len(positional) == 0 || len(positional) > 2 {
		return errors.New(deviceUsage)
	}
	devices, problems := collectDevices(c, *remoteName)
	var device *reachableDevice
	for i := range devices {
		if devices[i].Name == positional[0] {
			device = &devices[i]
		}
	}
	if device == nil {
		printDevices(devices, problems)
		return fmt.Errorf("device %s is not reachable", positional[0])
	}
	if device.Error != "" {
		return errors.New(device.Error)
	}
	folder, err := chooseDeviceFolder(device.Folders, positional[1:])
	if err != nil {
		return err
	}
	remote := c.Remotes[device.Remote]
	connect := func(thread string) (*appConnection, remoteChatServerReady, error) {
		return connectDeviceFolder(remote, device.Name, folder.Folder, thread)
	}
	connection, ready, err := connect("")
	if err != nil {
		return err
	}
	chatPlace = chatPlacement{remote: device.Remote}
	noteChatRunner(ready)
	conversation := ready.Conversation
	if *fresh {
		conversation = ""
	}
	options := chatLoopOptions{
		cwd: ready.CWD, id: conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		sharedTurnSelection: ready.TurnSelection, reconnect: connect, notice: ready.Notice,
	}
	return chatLoopWithOptions(connection, os.Stdin, os.Stdout, options)
}

func chooseDeviceFolder(folders []deviceFolder, args []string) (deviceFolder, error) {
	if len(folders) == 0 {
		return deviceFolder{}, errors.New("the device has no folders opened with Jara")
	}
	target := ""
	if len(args) == 1 {
		target = args[0]
	} else if len(folders) == 1 {
		return folders[0], nil
	} else {
		for i, folder := range folders {
			fmt.Printf("  %d  %s\n", i+1, folder.Folder)
		}
		fmt.Printf("Open which folder? [1-%d] ", len(folders))
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		target = strings.TrimSpace(line)
	}
	if n, err := strconv.Atoi(target); err == nil && n >= 1 && n <= len(folders) {
		return folders[n-1], nil
	}
	var matches []deviceFolder
	for _, folder := range folders {
		if folder.Folder == target || filepath.Base(folder.Folder) == target {
			matches = append(matches, folder)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return deviceFolder{}, fmt.Errorf("several folders are named %s; give the full path or number", target)
	}
	return deviceFolder{}, fmt.Errorf("the device has not opened %q with Jara (jara device list)", target)
}
