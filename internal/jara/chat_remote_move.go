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
// a remote host. The window stays open: the old place hands the workspace
// over, and the window reconnects to the new place's session the way it
// reconnects after a dropped connection, reloading the conversation.
//
//   - This device to the folder's host: the local session saves a final
//     checkpoint and frees the workspace (jara/release); once it ended, the
//     window joins the host's session, which continues the conversation.
//   - The host to this device: the window detaches and claims the workspace
//     here, catching up from the host's newest checkpoint; while the host is
//     still busy it watches live until the host hands back.
//   - A host the folder is not attached to, while the agent runs here: Jara
//     opens again with --remote NAME, which attaches the folder there (it may
//     ask to clone), sending the conversations along; the agent keeps
//     running here until /remote moves it.
//   - One host straight to another is refused: the newest state lives on the
//     first host, so the conversation comes to this device in between.

// chatPlacement says where this window's agent runs.
type chatPlacement struct {
	root    string // the folder
	remote  string // the host running the agent; "" when it runs here
	session string // this device's session, when it runs here
}

var chatPlace chatPlacement

// chatMoves is what the client knows when it opens the window: how to reach
// this folder's session on this device and on its host.
var chatMoves struct {
	here        func(thread string, explicit bool) (*appConnection, remoteChatServerReady, error) // nil when this device cannot run the agent
	hereSession string
	host        chatReconnector // nil without a host
	hostName    string
}

// chatMove is the reopen /remote asked for to attach another host; Run
// starts it once the window has closed.
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
		here := chatMoves.here
		if here == nil {
			return refuse("This device cannot run the agent here: it needs codex on PATH and the Jara interface.")
		}
		if err := u.sendPersistentExit("jara/detach"); err != nil {
			return false, err
		}
		chatPlace.remote, chatPlace.session = "", chatMoves.hereSession
		u.moveConnection(
			func(thread string) (*appConnection, remoteChatServerReady, error) { return here(thread, true) },
			func(thread string) (*appConnection, remoteChatServerReady, error) { return here(thread, false) },
			"Moving to this device", "Moving the conversation to this device…\n", "The conversation now runs on this device.")
		return false, nil
	case place.remote != "":
		return refuse("The conversation runs on " + place.remote + ". Bring it to this device first (/remote here), then move it to " + target + ".")
	case target == c.Bindings[place.root].Remote && chatMoves.host != nil && chatMoves.hostName == target:
		if err := u.sendPersistentExit("jara/release"); err != nil {
			return false, err
		}
		host, handingOver := chatMoves.host, place.session
		chatPlace.remote, chatPlace.session = target, ""
		u.moveConnection(
			func(thread string) (*appConnection, remoteChatServerReady, error) {
				waitForChatSessionEnd(handingOver, 30*time.Second) // its final checkpoint first
				return host(thread)
			}, host,
			"Moving to "+target, "Moving the conversation to "+target+"…\n", "The conversation now runs on "+target+".")
		return false, nil
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

// waitForChatSessionEnd waits until this device's session id ended, as it
// does after handing the workspace over, or until timeout.
func waitForChatSessionEnd(id string, timeout time.Duration) {
	if id == "" {
		return
	}
	paths, err := chatDaemonPathSet(id)
	if err != nil {
		return
	}
	for deadline := time.Now().Add(timeout); chatDaemonPID(paths) != 0 && time.Now().Before(deadline); {
		time.Sleep(200 * time.Millisecond)
	}
}

// reopen starts Jara where the conversation moved, in this terminal, once
// this device's session has handed the workspace over.
func (m *chatMoveRequest) reopen() error {
	if m.waitFor != "" {
		fmt.Fprintln(os.Stderr, "Handing the conversation over…")
		waitForChatSessionEnd(m.waitFor, 30*time.Second)
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

// noteChatMoves records how the window opening now can reach this folder's
// session on its host, and on this device when it can run the agent.
func noteChatMoves(remote Remote, name string, local localRunnerOpen, q Request, hereAvailable bool) {
	host := q
	host.Reconnect = !q.Worktree
	chatMoves.host = func(thread string) (*appConnection, remoteChatServerReady, error) {
		return reconnectRemoteJaraClient(remote, host, thread)
	}
	chatMoves.hostName, chatMoves.here, chatMoves.hereSession = name, nil, ""
	if !hereAvailable || q.Worktree {
		return
	}
	here, err := localRunnerRequest(local, q)
	if err != nil {
		return
	}
	chatMoves.hereSession = here.ID
	chatMoves.here = func(thread string, explicit bool) (*appConnection, remoteChatServerReady, error) {
		return connectLocalRunner(local, chatRequestFor(here, thread), explicit)
	}
}

// chatHereRunner marks a connection to this device's own session.
const chatHereRunner = "\x00here"

// noteChatRunner follows where the agent runs when a connection says so:
// here, or on the host or device this window watches until it hands back.
func noteChatRunner(ready remoteChatServerReady) {
	switch ready.Runner {
	case "":
	case chatHereRunner:
		chatPlace.remote = ""
		if chatPlace.session == "" {
			chatPlace.session = chatMoves.hereSession
		}
	default:
		chatPlace.remote = ready.Runner
	}
}
