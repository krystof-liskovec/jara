package jara

import "fmt"

type earlyClientAction struct {
	action, remoteName, port string
	remote                   Remote
	target, bind             *string
	forwards, reverses       portSpecs
	forwardTarget            *string
	reverseTarget            *string
	localBind                *string
	remoteBind               *string
	openSession              func(Request) error
}

func (action earlyClientAction) run() error {
	switch action.action {
	case "doctor":
		return doctor(action.remoteName, action.remote)
	case "forward", "reverse":
		return forward(action.remote, action.action, action.port, *action.target, *action.bind)
	case "tunnel":
		forwards, err := parsePortSpecs(action.forwards)
		if err != nil {
			return fmt.Errorf("--forward: %w", err)
		}
		reverses, err := parsePortSpecs(action.reverses)
		if err != nil {
			return fmt.Errorf("--reverse: %w", err)
		}
		return openTunnel(action.remote, forwards, reverses, *action.forwardTarget, *action.reverseTarget, *action.localBind, *action.remoteBind)
	case "sessions":
		return action.openSession(Request{})
	default:
		return nil
	}
}
