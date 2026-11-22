package jara

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// From `jara connect` on a host, the devices linked to it (jara device allow)
// are one more place to work: choose one, then a folder it has opened with
// Jara, and join its session or start a new conversation there. The host
// relays the session over the device's link, as for jara device open.

// onlineDeviceNames lists the devices linked to this host right now, without
// asking them anything.
func onlineDeviceNames() []string {
	entries, _ := os.ReadDir(deviceHubDir())
	var names []string
	for _, entry := range entries {
		if name, ok := strings.CutSuffix(entry.Name(), ".json"); ok {
			if record, found := loadDeviceRecord(name); found && record.online() {
				names = append(names, name)
			}
		}
	}
	sort.Strings(names)
	return names
}

func browseDevices() (menuItem, error) {
	for {
		devices := hubDevices()
		sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
		rows := make([]menuItem, len(devices))
		for i, device := range devices {
			rows[i] = menuItem{Title: device.Name, Detail: deviceDetail(device), Kind: "device"}
		}
		n, err := selectMenu("Devices", rows)
		if err != nil {
			return menuItem{}, err
		}
		if devices[n].Error != "" {
			if err = showMenuNotice(devices[n].Name, devices[n].Error); err != nil {
				return menuItem{}, err
			}
			continue
		}
		chosen, err := browseDeviceFolders(devices[n])
		if errors.Is(err, io.EOF) {
			continue
		}
		return chosen, err
	}
}

func deviceDetail(device deviceInfo) string {
	if device.Error != "" {
		return "Unavailable: " + device.Error
	}
	running, busy := 0, 0
	for _, folder := range device.Folders {
		if folder.Running {
			running++
		}
		if folder.Busy {
			busy++
		}
	}
	detail := fmt.Sprintf("%d folders", len(device.Folders))
	if running > 0 {
		detail += fmt.Sprintf(" · %d running", running)
	}
	if busy > 0 {
		detail += fmt.Sprintf(" · %d working", busy)
	}
	return detail
}

func browseDeviceFolders(device deviceInfo) (menuItem, error) {
	if len(device.Folders) == 0 {
		return menuItem{}, showMenuNotice(device.Name, "No folders opened with Jara on this device", "Run jara in a folder there first")
	}
	rows := make([]menuItem, len(device.Folders))
	for i, folder := range device.Folders {
		detail := folder.Folder
		if folder.Busy {
			detail = "working · " + detail
		} else if folder.Running {
			detail = "running · " + detail
		}
		rows[i] = menuItem{Title: filepath.Base(folder.Folder), Detail: detail, Kind: "device-folder"}
	}
	for {
		n, err := selectMenu(device.Name, rows)
		if err != nil {
			return menuItem{}, err
		}
		folder := device.Folders[n]
		open := menuItem{Title: filepath.Base(folder.Folder), Kind: "device-session", Group: device.Name, Path: folder.Folder}
		if !folder.Running {
			return open, nil // starting it continues the folder's conversation, like jara there
		}
		join, fresh := open, open
		join.Title, join.Detail = "Join the running session", "Continue its conversation"
		fresh.Title, fresh.Detail, fresh.Action = "New conversation", "In the running session", "new"
		i, err := selectMenu(device.Name+" · "+open.Title, []menuItem{join, fresh})
		if errors.Is(err, io.EOF) {
			continue
		}
		if err != nil {
			return menuItem{}, err
		}
		return []menuItem{join, fresh}[i], nil
	}
}

// openHubDeviceSession runs Jara's conversation interface here for a
// session on a linked device: the device starts or joins it, this host relays.
func openHubDeviceSession(device, folder string, fresh bool) error {
	connect := func(thread string) (*appConnection, remoteChatServerReady, error) {
		return connectHubDevice(device, folder, thread)
	}
	connection, ready, err := connect("")
	if err != nil {
		return err
	}
	noteChatRunner(ready)
	conversation := ready.Conversation
	if fresh {
		conversation = ""
	}
	options := chatLoopOptions{
		cwd: ready.CWD, id: conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		sharedTurnSelection: ready.TurnSelection, reconnect: connect, notice: ready.Notice,
	}
	return chatLoopWithOptions(connection, os.Stdin, os.Stdout, options)
}

func connectHubDevice(device, folder, thread string) (*appConnection, remoteChatServerReady, error) {
	conn, err := dialDevice(device)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	if err = json.NewEncoder(conn).Encode(deviceRequest{Op: "open", Folder: folder, Conversation: thread}); err != nil {
		conn.Close()
		return nil, remoteChatServerReady{}, err
	}
	connection, ready, err := readyLocalRunner(conn, fmt.Sprintf("Attached to the session on %s; it keeps running there when you close this window.", device))
	if err != nil {
		return nil, ready, err
	}
	ready.Runner = device
	return connection, ready, nil
}
