package jara

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"krystof.liskovec/jara/internal/fsutil"
)

// A host learns the name you call it by from your Jara client: setup,
// `jara remote add` and `jara doctor` send it with their check, and every
// session sends it too (the name in your Jara configuration, such as
// jellyfish). Chat windows running on the host itself,
// such as after `ssh` and `jara connect`, then show it next to the controls.
// `jara config set host-name NAME` on the host sets it by hand; `auto` goes
// back to learning it.

type hostNameRecord struct {
	Name   string `json:"name"`
	Manual bool   `json:"manual,omitempty"`
}

func hostNamePath() string { return filepath.Join(fsutil.DataRoot(), "host-name.json") }

func loadHostName() hostNameRecord {
	var record hostNameRecord
	if data, err := os.ReadFile(hostNamePath()); err == nil {
		_ = json.Unmarshal(data, &record)
	}
	if !nameRE.MatchString(record.Name) {
		record.Name = ""
	}
	return record
}

// rememberHostName keeps the name a client knows this host by, unless one
// was set by hand.
func rememberHostName(name string) {
	if !nameRE.MatchString(name) {
		return
	}
	if record := loadHostName(); record.Manual || record.Name == name {
		return
	}
	_ = fsutil.WriteJSON(hostNamePath(), hostNameRecord{Name: name})
}

// setHostName names this host by hand; "auto" learns it from clients again.
func setHostName(name string) error {
	if name == "auto" {
		record := loadHostName()
		record.Manual = false
		if err := fsutil.WriteJSON(hostNamePath(), record); err != nil {
			return err
		}
		fmt.Println("This host's name is learned from the clients that connect to it again.")
		return nil
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("a host name uses letters, digits, - and _, starting with a letter or digit")
	}
	if err := fsutil.WriteJSON(hostNamePath(), hostNameRecord{Name: name, Manual: true}); err != nil {
		return err
	}
	fmt.Printf("Chat windows on this host show it as %s.\n", name)
	return nil
}

var chatHostName = sync.OnceValue(func() string { return loadHostName().Name })

// chatControlHost is the host the controls name: where the agent runs when
// that is a remote host, or this host itself for windows running on it.
func chatControlHost() string {
	if chatPlace.remote != "" || chatPlace.root != "" {
		return chatPlace.remote // a client window knows its place
	}
	return chatHostName()
}
