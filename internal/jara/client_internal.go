package jara

import (
	"fmt"
	"os"
)

// runInternalCommand runs the hidden commands Jara invokes on itself, locally
// or over SSH. It reports whether args named one.
func runInternalCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "_rpc", "_open":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid internal invocation")
		}
		if args[0] == "_open" {
			return true, openRequest(args[1])
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, remoteRPC(q)
	case "_peer", "_peer-keys":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid internal invocation")
		}
		if args[0] == "_peer-keys" {
			return true, runPeerKeys(args[1])
		}
		return true, runPeerCommand(args[1], os.Stdin)
	case "_standby-forward":
		var q Request
		if len(args) != 2 || decode(args[1], &q) != nil {
			return true, fmt.Errorf("invalid internal invocation")
		}
		return true, runStandbyForward(q.ID)
	case "_client-binary":
		return true, streamClientBinary()
	case "_terminal":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid internal invocation")
		}
		var codexArgs []string
		if err := decode(args[1], &codexArgs); err != nil {
			return true, err
		}
		return true, runCodexTerminal(codexArgs)
	case "_background", "_chat":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid background Codex invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		if args[0] == "_chat" {
			return true, runJaraChat(q)
		}
		return true, runBackgroundCodex(q)
	case "_chat-server":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid remote app-server invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, runRemoteChatServer(q)
	case "_chat-daemon":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid persistent Jara session invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, runChatDaemon(q)
	case "_runner-link":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid runner link invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, runRunnerLink(q)
	case "_runner-watch":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid runner watch invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, runRunnerWatch(q)
	case "_runner-daemon":
		if len(args) != 2 {
			return true, fmt.Errorf("invalid local runner invocation")
		}
		var q Request
		if err := decode(args[1], &q); err != nil {
			return true, err
		}
		return true, runLocalRunnerDaemon(q)
	}
	return false, nil
}
