package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

type chatPending struct {
	method   string
	deadline time.Time
}
type chatUI struct {
	c                        *appConnection
	out                      io.Writer
	cwd, thread, turn        string
	busy, cancel             bool
	threadReady              bool
	connected                bool
	composing                bool
	draft                    []string
	draftBytes               int
	startedAt                time.Time
	turnStartedAt            time.Time
	lastAgentState           string
	effectiveModel           string
	nextModel                string
	nextModelLabel           string
	modelSelectionGeneration uint64
	pending                  map[string]chatPending
	pendingTurnModels        map[string]chatTurnRequest
	approvals                []appMessage
	questionRequests         []chatQuestionRequest
	sessions                 *chatSessionBrowser
	models                   *chatModelBrowser
	items                    map[string]json.RawMessage
}

// Text from the model and tools is untrusted terminal content.
func chatText(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 && r != 127 && !(r >= 128 && r <= 159) {
			return r
		}
		return -1
	}, s)
}
func (u *chatUI) print(s string) { fmt.Fprint(u.out, chatText(s)) }
func (u *chatUI) printRole(role, s string) {
	s = chatText(s)
	if writer, ok := u.out.(interface{ WriteChatRole(string, string) }); ok {
		writer.WriteChatRole(role, s)
		return
	}
	fmt.Fprint(u.out, s)
}
func (u *chatUI) recordRole(role, s string) {
	if writer, ok := u.out.(interface{ WriteChatRole(string, string) }); ok {
		writer.WriteChatRole(role, chatText(s))
	}
}
func (u *chatUI) request(method string, params any) error {
	id, err := u.c.request(method, params)
	if err != nil {
		return err
	}
	u.pending[id] = chatPending{method, time.Now().Add(30 * time.Second)}
	return nil
}
func (u *chatUI) setAgentState(state string) {
	if state == "resuming" {
		state = "starting"
	}
	if u.lastAgentState == state {
		return
	}
	u.lastAgentState = state
	publishChatAgentState(state)
}
func publishChatAgentState(state string) {
	if os.Getenv("TMUX") == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return
	}
	_ = tmux("set-option", "-t", pane, "@jara_agent_pid", strconv.Itoa(os.Getpid())).Run()
	_ = tmux("set-option", "-t", pane, "@jara_agent_state", state).Run()
}
func clearChatAgentState() {
	if os.Getenv("TMUX") == "" {
		return
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return
	}
	_ = tmux("set-option", "-u", "-t", pane, "@jara_agent_state").Run()
	_ = tmux("set-option", "-u", "-t", pane, "@jara_agent_pid").Run()
}
func (u *chatUI) open(id string) error {
	u.busy = true
	u.setAgentState("starting")
	params := map[string]any{"cwd": u.cwd}
	method := "thread/start"
	if id != "" {
		method = "thread/resume"
		params["threadId"] = id
	}
	return u.request(method, params)
}
func (u *chatUI) interrupt() error {
	for _, p := range u.pending {
		if p.method == "initialize" || p.method == "thread/start" || p.method == "thread/resume" {
			u.print("Conversation is still starting. Wait for it to open before cancelling a turn.\n")
			return nil
		}
	}
	if err := u.cancelQuestionRequests("Question request cancelled because the turn was interrupted."); err != nil {
		return err
	}
	u.cancel = true
	for _, m := range u.approvals {
		if err := u.c.reply(m.ID, map[string]string{"decision": "cancel"}); err != nil {
			return err
		}
	}
	u.approvals = nil
	if u.turn != "" {
		u.cancel = false
		u.setAgentState("running")
		return u.request("turn/interrupt", map[string]string{"threadId": u.thread, "turnId": u.turn})
	}
	if u.busy {
		u.setAgentState("running")
	} else {
		u.setAgentState("idle")
	}
	return nil
}
func (u *chatUI) response(m appMessage) error {
	p, ok := u.pending[string(m.ID)]
	if !ok {
		return nil
	}
	delete(u.pending, string(m.ID))
	if m.Error != nil {
		if p.method == "thread/list" {
			err := fmt.Errorf("Codex app-server rejected thread/list: %s", chatText(m.Error.Message))
			if !u.failChatSessionRequest(err) {
				u.print("\nSaved session lookup failed: " + chatText(m.Error.Message) + "\n")
				if !u.busy {
					u.print("You> ")
				}
			}
			return nil
		}
		if p.method == "model/list" {
			err := fmt.Errorf("Codex app-server rejected model/list: %s", chatText(m.Error.Message))
			if !u.failChatModelRequest(err) {
				u.print("\nModel lookup failed: " + chatText(m.Error.Message) + "\n")
				if !u.busy {
					u.print("You> ")
				}
			}
			return nil
		}
		if p.method == "turn/start" {
			request, tracked := u.pendingTurnModels[string(m.ID)]
			delete(u.pendingTurnModels, string(m.ID))
			if tracked && request.thread != u.thread {
				return nil
			}
		}
		if recoverChatRequestError(u, p, m) {
			return nil
		}
		return fmt.Errorf("%s: %s", p.method, chatText(m.Error.Message))
	}
	switch p.method {
	case "initialize":
		u.connected = true
		if err := u.c.encoder.Encode(map[string]string{"method": "initialized"}); err != nil {
			return err
		}
		return u.open(u.thread)
	case "thread/start", "thread/resume":
		var r struct {
			Thread struct {
				ID    string `json:"id"`
				Turns []struct{ Items []struct{ Type, Text string } }
			}
			Model string `json:"model"`
		}
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if r.Thread.ID == "" {
			return fmt.Errorf("app-server returned an empty thread ID")
		}
		previousThread := u.thread
		u.thread = r.Thread.ID
		if previousThread != "" && previousThread != u.thread {
			u.nextModel = ""
			u.nextModelLabel = ""
		}
		u.setEffectiveModel(r.Model)
		u.threadReady = true
		u.busy = false
		u.setAgentState("idle")
		// Explicit metadata links this terminal to its saved conversation.
		if os.Getenv("TMUX") != "" {
			_ = tmux("set-option", "-t", os.Getenv("TMUX_PANE"), "@jara_codex_thread", u.thread).Run()
		}
		u.print("\nConversation " + u.thread + "\n")
		for _, t := range r.Thread.Turns {
			for _, item := range t.Items {
				if item.Type == "agentMessage" {
					u.printRole("assistant", item.Text+"\n")
				}
			}
		}
		u.print("\nYou> ")
	case "thread/list":
		return u.acceptChatSessionPage(m.Result)
	case "model/list":
		return u.acceptChatModelPage(m.Result)
	case "turn/start":
		var r struct{ Turn struct{ ID string } }
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if request, ok := u.pendingTurnModels[string(m.ID)]; ok {
			delete(u.pendingTurnModels, string(m.ID))
			if request.thread != u.thread {
				return nil
			}
			if request.model != "" {
				u.setEffectiveModel(request.model)
				if request.generation == u.modelSelectionGeneration {
					u.nextModel = ""
					u.nextModelLabel = ""
				}
			}
		}
		if u.busy {
			u.turn = r.Turn.ID
			u.turnStartedAt = time.Now()
			u.setAgentState(u.currentState())
			if u.cancel {
				return u.interrupt()
			}
		}
	}
	return nil
}
func (u *chatUI) event(m appMessage) error {
	if m.Method == "" {
		return u.response(m)
	}
	var p struct {
		Delta   string
		Message string
		Turn    struct {
			ID, Status string
			Error      *struct{ Message string }
		}
		Item   json.RawMessage
		ItemID string `json:"itemId"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		return err
	}
	if len(m.ID) > 0 {
		switch m.Method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			u.approvals = append(u.approvals, m)
			u.setAgentState("waiting")
			u.print(fmt.Sprintf("\nApproval required (%d pending)\n%sType /approve to allow once or /deny to reject.\n", len(u.approvals), formatChatApproval(m, u.items[p.ItemID])))
			return nil
		case "item/tool/requestUserInput":
			return u.handleQuestionRequest(m)
		default:
			u.print("\nUnsupported interaction: " + m.Method + "\n")
			return u.c.unsupported(m)
		}
	}
	switch m.Method {
	case "turn/started":
		u.turn = p.Turn.ID
		u.turnStartedAt = time.Now()
		u.setAgentState(u.currentState())
		if u.cancel {
			return u.interrupt()
		}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		role := "assistant"
		if m.Method == "item/commandExecution/outputDelta" {
			role = "tool"
		}
		u.printRole(role, p.Delta)
	case "item/started", "item/completed":
		var item struct{ ID, Type, Command, Status string }
		_ = json.Unmarshal(p.Item, &item)
		u.items[item.ID] = p.Item
		if m.Method == "item/started" && item.Type == "commandExecution" {
			u.printRole("tool", "\n$ "+item.Command+"\n")
		}
		if item.Type == "fileChange" {
			u.printRole("tool", "\nFile changes: "+string(p.Item)+"\n")
		}
	case "turn/completed":
		if err := u.cancelQuestionRequests("Turn ended before the question was answered."); err != nil {
			return err
		}
		u.busy = false
		u.turn = ""
		u.turnStartedAt = time.Time{}
		u.cancel = false
		u.approvals = nil
		u.items = map[string]json.RawMessage{}
		u.setAgentState("idle")
		u.print("\n[" + p.Turn.Status + "]\n")
		if p.Turn.Error != nil {
			u.print(p.Turn.Error.Message + "\n")
		}
		u.print("\nYou> ")
	case "error":
		u.print("\nCodex: " + string(m.Params) + "\n")
	}
	return nil
}
