package jara

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// /remote moves this folder's conversation to another place: this device or
// a remote host. The window closes, the old place hands the workspace over,
// and Jara opens again in the same terminal where the conversation now runs.
//
//   - This device to the folder's host: the local session saves a final
//     checkpoint and frees the workspace (jara/release); then jara
//     --run-remote continues there.
//   - The host to this device: the window detaches and jara --local claims
//     the workspace back once the host is idle.
//   - A host the folder is not attached to, while the agent runs here: jara
//     --remote NAME attaches it there, sending the conversations along; the
//     agent keeps running here until /remote moves it.
//   - One host straight to another is refused: the newest state lives on the
//     first host, so the conversation comes to this device in between.

// chatPlacement says where this window's agent runs.
type chatPlacement struct {
	root    string // the folder
	remote  string // the host running the agent; "" when it runs here
	session string // this device's session, when it runs here
}

var chatPlace chatPlacement

// chatMove is the reopen /remote asked for; Run starts it once the window
// has closed.
var chatMove *chatMoveRequest

type chatMoveRequest struct {
	args    []string // jara arguments for the new place
	waitFor string   // a session on this device that must hand over first
}

const chatThisDevice = "This device"

// remoteCommand handles /remote [NAME | here].
func (u *chatUI) remoteCommand(arg string) (bool, error) {
	c, err := readConfig()
	if err != nil {
		u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
		return false, nil
	}
	if arg == "" {
		u.remotePlaces = chatRemotePlaces(c)
		if chatUIIsInteractive(u) {
			u.pickerRequested = "remote"
			return false, nil
		}
		var b strings.Builder
		b.WriteString("Places this conversation can run; move it with /remote NAME or /remote here:\n")
		for _, choice := range chatRemotePickerChoices(u, -1)[1:] {
			b.WriteString("- " + choice.Value + "  " + choice.Description + "\n")
		}
		u.printRole("system", b.String())
		return false, nil
	}
	return u.moveConversation(c, arg)
}

func (u *chatUI) moveConversation(c Config, target string) (bool, error) {
	place := chatPlace
	if target == "here" || target == "local" || target == chatThisDevice {
		target = ""
	}
	refuse := func(message string) (bool, error) {
		u.printRole("system", message+"\n")
		return false, nil
	}
	if _, known := c.Remotes[target]; target != "" && !known {
		return refuse("There is no remote named " + target + "; see jara remote list.")
	}
	switch {
	case place.root == "":
		return refuse("/remote moves a folder's conversation; open the folder with jara to use it.")
	case u.agentTurnActive():
		return refuse("A turn is running. Wait for it to finish or /cancel it, then move the conversation.")
	case target == place.remote:
		return refuse("The conversation already runs " + chatPlaceName(target) + ".")
	case target == "":
		chatMove = &chatMoveRequest{args: []string{"--local"}}
		u.printRole("system", "Moving the conversation to this device…\n")
		return true, u.sendPersistentExit("jara/detach")
	case place.remote != "":
		return refuse("The conversation runs on " + place.remote + ". Bring it to this device first (/remote here), then move it to " + target + ".")
	case target == c.Bindings[place.root].Remote:
		chatMove = &chatMoveRequest{args: []string{"--run-remote"}, waitFor: place.session}
		u.printRole("system", "Moving the conversation to "+target+"…\n")
		return true, u.sendPersistentExit("jara/release")
	default:
		chatMove = &chatMoveRequest{args: []string{"--remote", target}, waitFor: place.session}
		u.printRole("system", "Attaching this folder to "+target+"; the conversations go along and the agent keeps running here. Choose "+target+" in /remote again to run it there.\n")
		return true, u.sendPersistentExit("jara/close")
	}
}

func chatPlaceName(remote string) string {
	if remote == "" {
		return "on this device"
	}
	return "on " + remote
}

// chatRemotePlaces lists this device, then the remotes in their ranking.
func chatRemotePlaces(c Config) []string {
	places := []string{""}
	for _, name := range c.Order {
		if _, ok := c.Remotes[name]; ok && !slices.Contains(places, name) {
			places = append(places, name)
		}
	}
	for _, name := range sortedRemoteNames(c.Remotes) {
		if !slices.Contains(places, name) {
			places = append(places, name)
		}
	}
	return places
}

func chatRemotePickerChoices(u *chatUI, selected int) []chatCompletionChoice {
	c, _ := readConfig()
	attached := c.Bindings[chatPlace.root].Remote
	choices := []chatCompletionChoice{{Value: "Move this conversation", Description: "it runs " + chatPlaceName(chatPlace.remote) + " now"}}
	for index, place := range u.remotePlaces {
		value, notes := place, []string(nil)
		if place == "" {
			value = chatThisDevice
		}
		if place == chatPlace.remote {
			notes = append(notes, "runs here now")
		}
		if place != "" && place == attached {
			notes = append(notes, "this folder's host")
		}
		if place != "" && c.Remotes[place].Standby {
			notes = append(notes, "standby")
		}
		choices = append(choices, chatCompletionChoice{Value: value, Description: strings.Join(notes, " · "),
			Selected: index == selected, Index: index, Total: len(u.remotePlaces)})
	}
	return choices
}

// reopen starts Jara where the conversation moved, in this terminal, once
// this device's session has handed the workspace over.
func (m *chatMoveRequest) reopen() error {
	if m.waitFor != "" {
		if paths, err := chatDaemonPathSet(m.waitFor); err == nil {
			fmt.Fprintln(os.Stderr, "Handing the conversation over…")
			for deadline := time.Now().Add(30 * time.Second); chatDaemonPID(paths) != 0 && time.Now().Before(deadline); {
				time.Sleep(200 * time.Millisecond)
			}
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, m.args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("jara %s exited with status %s", strings.Join(m.args, " "), strconv.Itoa(exit.ExitCode()))
	}
	return err
}
