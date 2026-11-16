package jara

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func openRemoteJaraClient(remote Remote, request Request) (bool, error) {
	fmt.Fprintln(os.Stderr, "Connecting to persistent Jara session...")
	// A worktree session cannot be recreated in the same place, so it does not reconnect.
	request.Reconnect = !request.Worktree
	diagnostics := &chatDiagnosticBuffer{}
	command := sshCommand(remote, false, shellArgs("jara", "_chat-server", token(request)))
	connection, err := startChatServerCommand(command, diagnostics)
	if err != nil {
		return true, err
	}
	defer connection.close()
	ready, err := awaitRemoteChatReady(connection)
	if err != nil {
		detail := diagnostics.String()
		if strings.Contains(detail, `unknown command "_chat-server"`) {
			return false, nil
		}
		if detail != "" {
			return true, fmt.Errorf("start remote Jara app-server: %w\nRemote diagnostics:\n%s", err, chatText(detail))
		}
		return true, err
	}
	if ready.ManagedSession {
		return false, nil
	}
	if ready.CWD == "" {
		return true, fmt.Errorf("remote Jara app-server returned no workspace path")
	}
	if ready.Workspace == "" {
		cwd, cwdErr := os.Getwd()
		if cwdErr == nil {
			ready.Workspace = filepath.Base(cwd)
		}
	}
	conversation := request.Conversation
	if conversation == "" && !request.New && !request.Resume && !request.ResumeLatest {
		conversation = ready.Conversation
	}
	options := chatLoopOptions{
		cwd: ready.CWD, id: conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		resumeAtStart: request.Conversation == "" && (request.Resume || request.ResumeLatest),
		resumeLatest:  request.Conversation == "" && request.ResumeLatest,
	}
	if request.Reconnect {
		options.reconnect = func(thread string) (*appConnection, remoteChatServerReady, error) {
			return reconnectRemoteJaraClient(remote, request, thread)
		}
	}
	err = chatLoopWithOptions(connection, os.Stdin, os.Stdout, options)
	if err != nil && diagnostics.String() != "" {
		return true, fmt.Errorf("%w\nRemote Codex diagnostics:\n%s", err, chatText(diagnostics.String()))
	}
	return true, err
}

func runRemoteChatClient(request Request) error {
	if err := validateExistingChatDaemonRequest(request); err != nil {
		return err
	}
	if connection, err := hubRunnerRoute(&request); err != nil {
		return err
	} else if connection != nil {
		return runRemoteChatClientConnection(request, connection)
	}
	connection, err := connectExistingChatDaemon(request.ID)
	if err != nil {
		return err
	}
	return runRemoteChatClientConnection(request, connection)
}

func runRemoteChatClientConnection(request Request, connection net.Conn) error {
	fmt.Fprintln(os.Stderr, "Connecting to persistent Jara session...")
	app := startChatConnection(connection)
	defer app.close()
	ready, err := awaitRemoteChatReady(app)
	if err != nil {
		return err
	}
	if ready.CWD == "" {
		return fmt.Errorf("remote Jara session returned no workspace path")
	}
	conversation := request.Conversation
	if conversation == "" && !request.New && !request.Resume && !request.ResumeLatest {
		conversation = ready.Conversation
	}
	options := chatLoopOptions{
		cwd: ready.CWD, id: conversation, workspaceLabel: ready.Workspace,
		persistentSession: true, initialized: ready.Initialized, attachedTurnActive: ready.ActiveTurn,
		resumeAtStart: request.Conversation == "" && (request.Resume || request.ResumeLatest),
		resumeLatest:  request.Conversation == "" && request.ResumeLatest,
	}
	return chatLoopWithOptions(app, os.Stdin, os.Stdout, options)
}

func awaitRemoteChatReady(connection *appConnection) (remoteChatServerReady, error) {
	select {
	case message := <-connection.messages:
		return decodeRemoteChatReady(message)
	case err := <-connection.errors:
		select {
		case message := <-connection.messages:
			return decodeRemoteChatReady(message)
		default:
		}
		return remoteChatServerReady{}, fmt.Errorf("remote app-server startup ended: %w", err)
	}
}

func decodeRemoteChatReady(message appMessage) (remoteChatServerReady, error) {
	if message.Method == "jara/error" {
		var failure struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(message.Params, &failure); err != nil {
			return remoteChatServerReady{}, fmt.Errorf("decode remote Jara session error: %w", err)
		}
		return remoteChatServerReady{}, fmt.Errorf("remote Jara session: %s", chatText(failure.Message))
	}
	if message.Method != "jara/ready" {
		return remoteChatServerReady{}, fmt.Errorf("remote app-server did not provide workspace metadata")
	}
	var ready remoteChatServerReady
	if err := json.Unmarshal(message.Params, &ready); err != nil {
		return ready, fmt.Errorf("decode remote app-server metadata: %w", err)
	}
	return ready, nil
}

// reconnectRemoteJaraClient reopens the SSH relay to the workspace's session,
// starting it again on thread if it stopped.
func reconnectRemoteJaraClient(remote Remote, request Request, thread string) (*appConnection, remoteChatServerReady, error) {
	request.Conversation = thread
	request.New, request.Resume, request.ResumeLatest = false, false, false
	request.Prompt, request.BackgroundPromptID = "", ""
	diagnostics := &chatDiagnosticBuffer{}
	command := sshCommand(remote, false, shellArgs("jara", "_chat-server", token(request)))
	connection, err := startChatServerCommand(command, diagnostics)
	if err != nil {
		return nil, remoteChatServerReady{}, err
	}
	ready, err := awaitRemoteChatReady(connection)
	if err == nil && ready.ManagedSession {
		err = fmt.Errorf("a terminal session now runs in this workspace")
	}
	if err == nil && ready.CWD == "" {
		err = fmt.Errorf("remote Jara app-server returned no workspace path")
	}
	if err != nil {
		connection.close()
		if detail := strings.TrimSpace(diagnostics.String()); detail != "" {
			err = fmt.Errorf("%w: %s", err, chatText(detail))
		}
		return nil, remoteChatServerReady{}, err
	}
	return connection, ready, nil
}
