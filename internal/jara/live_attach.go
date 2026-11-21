package jara

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// psCommand is `jara ps [--remote NAME | --local]`: every running Jara
// session on this device and on the remote hosts, including sessions other
// devices run through a remote host.
func psCommand(c Config, args []string) error {
	f := flag.NewFlagSet("ps", flag.ContinueOnError)
	remote := f.String("remote", "", "list only the sessions a remote host reaches")
	local := f.Bool("local", false, "list only this device's sessions")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *local && *remote != "" {
		return fmt.Errorf("use jara ps [--remote NAME | --local]")
	}
	sessions, problems, err := listLiveSessions(c, *remote, *local)
	if err != nil {
		return err
	}
	printLiveSessions(sessions, problems)
	return nil
}

func listLiveSessions(c Config, remote string, localOnly bool) ([]liveSession, []string, error) {
	if localOnly {
		return localLiveSessions(), nil, nil
	}
	if remote != "" {
		if _, ok := c.Remotes[remote]; !ok {
			return nil, nil, fmt.Errorf("remote %s is not configured", remote)
		}
	}
	sessions, problems := collectLiveSessions(c, remote, remote == "")
	return sessions, problems, nil
}

// attachCommand is `jara attach [N|ID|NAME|FOLDER] [--remote NAME]`: open a
// window on a running session where it runs. Nothing moves: the session
// keeps running on its machine, and closing the window leaves it there.
func attachCommand(c Config, args []string) error {
	f := flag.NewFlagSet("attach", flag.ContinueOnError)
	remote := f.String("remote", "", "only consider sessions a remote host reaches")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() > 1 {
		return fmt.Errorf("use jara attach [N|ID|NAME|FOLDER] [--remote NAME]")
	}
	sessions, problems, err := listLiveSessions(c, *remote, false)
	if err != nil {
		return err
	}
	session, err := chooseLiveSession(sessions, problems, f.Arg(0), os.Stdin)
	if err != nil {
		return err
	}
	return attachLiveSession(c, session)
}

// chooseLiveSession resolves what `jara attach` was given: a number from
// `jara ps`, a session or workspace ID, a workspace name, or a folder. Without
// one it takes the current folder's session, the only one running, or asks.
func chooseLiveSession(sessions []liveSession, problems []string, target string, input io.Reader) (liveSession, error) {
	if len(sessions) == 0 {
		printLiveSessions(nil, problems)
		return liveSession{}, errors.New("nothing to attach to")
	}
	if target == "" {
		if cwd, err := os.Getwd(); err == nil {
			if resolved, e := filepath.EvalSymlinks(cwd); e == nil {
				cwd = resolved
			}
			for _, dir := range []string{cwd, gitTopLevel(cwd)} {
				if found := sessionsInFolder(sessions, dir); len(found) == 1 {
					return found[0], nil
				}
			}
		}
		if len(sessions) == 1 {
			return sessions[0], nil
		}
		printLiveSessions(sessions, problems)
		fmt.Printf("Attach to which session? [1-%d] ", len(sessions))
		line, err := bufio.NewReader(input).ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return liveSession{}, errors.New("no session chosen")
		}
		target = strings.TrimSpace(line)
	}
	if n, err := strconv.Atoi(target); err == nil {
		if n < 1 || n > len(sessions) {
			return liveSession{}, fmt.Errorf("there is no session %d; see jara ps", n)
		}
		return sessions[n-1], nil
	}
	var matches []liveSession
	for _, s := range sessions {
		if s.Local == target || s.Workspace == target || s.target() == target {
			return s, nil
		}
		if s.Name == target {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		if folder, err := filepath.Abs(target); err == nil {
			if resolved, e := filepath.EvalSymlinks(folder); e == nil {
				folder = resolved
			}
			matches = sessionsInFolder(sessions, folder)
		}
	}
	switch len(matches) {
	case 0:
		return liveSession{}, fmt.Errorf("no running session matches %q; see jara ps", target)
	case 1:
		return matches[0], nil
	}
	return liveSession{}, fmt.Errorf("several running sessions match %q; attach by number or ID (see jara ps)", target)
}

func sessionsInFolder(sessions []liveSession, folder string) []liveSession {
	var found []liveSession
	for _, s := range sessions {
		if folder != "" && s.Folder == folder {
			found = append(found, s)
		}
	}
	return found
}

// attachLiveSession opens the chat window on session. When the session ends
// (it moved, or its machine stopped it), the window follows the conversation
// to wherever it runs now, still without moving it.
func attachLiveSession(c Config, session liveSession) error {
	connect := func(thread string) (*appConnection, remoteChatServerReady, error) {
		return connectLiveSession(c, session, thread)
	}
	connection, ready, err := connect("")
	if err != nil {
		return err
	}
	chatPlace = chatPlacement{root: session.Folder, remote: session.Remote}
	if ready.Runner == chatHereRunner {
		chatPlace.remote, chatPlace.session = "", session.Local
	}
	noteChatRunner(ready)
	options := chatLoopOptions{
		cwd: ready.CWD, id: ready.Conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		sharedTurnSelection: ready.TurnSelection, reconnect: connect, notice: ready.Notice,
	}
	return chatLoopWithOptions(connection, os.Stdin, os.Stdout, options)
}

// connectLiveSession connects to the session: directly when it runs on this
// device, otherwise through its remote host, which forwards to the device
// that runs it. A local session that stopped is followed to the remote host
// its folder is attached to.
func connectLiveSession(c Config, session liveSession, thread string) (*appConnection, remoteChatServerReady, error) {
	if session.Local != "" && chatDaemonRunning(session.Local) {
		conn, err := connectExistingChatDaemon(session.Local)
		if err != nil {
			return nil, remoteChatServerReady{}, err
		}
		return readyLocalRunner(conn, "Attached to this device's session; it keeps running when you close this window.")
	}
	if session.DeviceFolder != "" {
		remote, ok := c.Remotes[session.Remote]
		if !ok {
			return nil, remoteChatServerReady{}, fmt.Errorf("remote %s is not configured", session.Remote)
		}
		return connectDeviceFolder(remote, session.Runner, session.DeviceFolder, thread)
	}
	if session.Remote == "" || session.Workspace == "" {
		return nil, remoteChatServerReady{}, errors.New("the session is no longer running")
	}
	remote, ok := c.Remotes[session.Remote]
	if !ok {
		return nil, remoteChatServerReady{}, fmt.Errorf("remote %s is not configured", session.Remote)
	}
	wire := Request{UserInterface: "jara", ID: session.Workspace, Conversation: thread, Reconnect: true, Attach: true, RemoteName: session.Remote}
	diagnostics := &chatDiagnosticBuffer{}
	connection, err := startChatServerCommand(sshCommand(remote, false, shellArgs("jara", "_chat-server", token(wire))), diagnostics)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	ready, err := awaitRemoteChatReady(connection)
	if err != nil {
		connection.close()
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			err = fmt.Errorf("%w: %s", err, chatText(detail))
		}
		return nil, ready, err
	}
	where := session.Remote
	if session.Local == "" && session.Runner != runnerHub && session.Runner != "" {
		where = session.Runner
	}
	ready.Runner = where
	ready.Notice = fmt.Sprintf("Attached to the session on %s; it keeps running there when you close this window.", where)
	return connection, ready, nil
}

// connectDeviceFolder starts or joins the session of a folder on a linked
// device, through a remote host the device keeps its link to.
func connectDeviceFolder(remote Remote, device, folder, thread string) (*appConnection, remoteChatServerReady, error) {
	wire := Request{Runner: device, Root: folder, Conversation: thread}
	diagnostics := &chatDiagnosticBuffer{}
	connection, err := startChatServerCommand(sshCommand(remote, false, shellArgs("jara", "_device-open", token(wire))), diagnostics)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	ready, err := awaitRemoteChatReady(connection)
	if err != nil {
		connection.close()
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" && !strings.Contains(err.Error(), "remote Jara session:") {
			err = fmt.Errorf("%w: %s", err, chatText(detail))
		}
		return nil, ready, err
	}
	ready.Runner = device
	ready.Notice = fmt.Sprintf("Attached to the session on %s; it keeps running there when you close this window.", device)
	return connection, ready, nil
}
