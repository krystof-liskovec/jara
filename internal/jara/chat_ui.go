package jara

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"
)

type chatPending struct {
	method   string
	deadline time.Time
}
type chatUI struct {
	c                 *appConnection
	out               io.Writer
	cwd, thread, turn string
	busy, cancel      bool
	pending           map[string]chatPending
	approvals         []appMessage
	items             map[string]json.RawMessage
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
func (u *chatUI) request(method string, params any) error {
	id, err := u.c.request(method, params)
	if err != nil {
		return err
	}
	u.pending[id] = chatPending{method, time.Now().Add(30 * time.Second)}
	return nil
}
func (u *chatUI) open(id string) error {
	u.busy = true
	params := map[string]any{"cwd": u.cwd}
	method := "thread/start"
	if id != "" {
		method = "thread/resume"
		params["threadId"] = id
	}
	return u.request(method, params)
}
func (u *chatUI) interrupt() error {
	u.cancel = true
	for _, m := range u.approvals {
		if err := u.c.reply(m.ID, map[string]string{"decision": "cancel"}); err != nil {
			return err
		}
	}
	u.approvals = nil
	if u.turn != "" {
		u.cancel = false
		return u.request("turn/interrupt", map[string]string{"threadId": u.thread, "turnId": u.turn})
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
		return fmt.Errorf("%s: %s", p.method, chatText(m.Error.Message))
	}
	switch p.method {
	case "initialize":
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
		}
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if r.Thread.ID == "" {
			return fmt.Errorf("app-server returned an empty thread ID")
		}
		u.thread = r.Thread.ID
		u.busy = false
		// Explicit metadata links this terminal to its saved conversation.
		if os.Getenv("TMUX") != "" {
			_ = tmux("set-option", "-t", os.Getenv("TMUX_PANE"), "@jara_codex_thread", u.thread).Run()
		}
		u.print("\nConversation " + u.thread + "\n")
		for _, t := range r.Thread.Turns {
			for _, item := range t.Items {
				if item.Type == "agentMessage" {
					u.print(item.Text + "\n")
				}
			}
		}
		u.print("\nYou> ")
	case "turn/start":
		var r struct{ Turn struct{ ID string } }
		if err := json.Unmarshal(m.Result, &r); err != nil {
			return err
		}
		if u.busy {
			u.turn = r.Turn.ID
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
			u.print("\nApproval requested: " + m.Method + "\n" + string(u.items[p.ItemID]) + "\n" + string(m.Params) + "\nType /approve to allow once or /deny to reject.\n")
			return nil
		default:
			u.print("\nUnsupported interaction: " + m.Method + "\n")
			return u.c.unsupported(m)
		}
	}
	switch m.Method {
	case "turn/started":
		u.turn = p.Turn.ID
		if u.cancel {
			return u.interrupt()
		}
	case "item/agentMessage/delta", "item/commandExecution/outputDelta":
		u.print(p.Delta)
	case "item/started", "item/completed":
		var item struct{ ID, Type, Command, Status string }
		_ = json.Unmarshal(p.Item, &item)
		u.items[item.ID] = p.Item
		if m.Method == "item/started" && item.Type == "commandExecution" {
			u.print("\n$ " + item.Command + "\n")
		}
		if item.Type == "fileChange" {
			u.print("\nFile changes: " + string(p.Item) + "\n")
		}
	case "turn/completed":
		u.busy = false
		u.turn = ""
		u.cancel = false
		u.approvals = nil
		u.items = map[string]json.RawMessage{}
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
func (u *chatUI) line(line string) (bool, error) {
	line = strings.TrimSpace(line)
	switch {
	case line == "/quit":
		return true, nil
	case line == "/cancel":
		return false, u.interrupt()
	case line == "/approve" || line == "/deny":
		if len(u.approvals) == 0 {
			u.print("No approval is pending.\n")
			return false, nil
		}
		decision := "decline"
		if line == "/approve" {
			decision = "accept"
		}
		m := u.approvals[0]
		u.approvals = u.approvals[1:]
		return false, u.c.reply(m.ID, map[string]string{"decision": decision})
	case u.busy:
		u.print("A turn is running. Use /cancel or wait for it to finish.\n")
		return false, nil
	case strings.HasPrefix(line, "/resume "):
		return false, u.open(strings.TrimSpace(strings.TrimPrefix(line, "/resume ")))
	case line == "/new":
		return false, u.open("")
	case line == "/help":
		u.print("/new · /resume ID · /cancel · /approve · /deny · /quit\n\nYou> ")
		return false, nil
	case line == "":
		u.print("You> ")
		return false, nil
	default:
		u.busy = true
		u.cancel = false
		return false, u.request("turn/start", map[string]any{"threadId": u.thread, "input": []any{map[string]any{"type": "text", "text": line}}})
	}
}
func chatLoop(c *appConnection, input io.Reader, output io.Writer, cwd, id string) error {
	u := &chatUI{c: c, out: output, cwd: cwd, thread: id, pending: map[string]chatPending{}, items: map[string]json.RawMessage{}}
	u.print("Jara conversation preview · /help for commands · Ctrl-C cancels a turn\n")
	if err := u.request("initialize", map[string]any{"clientInfo": map[string]string{"name": "jara", "title": "Jara", "version": Version}}); err != nil {
		return err
	}
	lines := make(chan string)
	inputErrors := make(chan error, 1)
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 4096), maxBackgroundPrompt)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-stop:
				return
			}
		}
		inputErrors <- scanner.Err()
	}()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt)
	defer signal.Stop(signals)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-c.messages:
			if err := u.event(m); err != nil {
				return err
			}
		case err := <-c.errors:
			return fmt.Errorf("Codex app-server disconnected: %w", err)
		case line := <-lines:
			quit, err := u.line(line)
			if err != nil {
				return err
			}
			if quit {
				return nil
			}
		case err := <-inputErrors:
			return err
		case <-signals:
			if u.busy {
				if err := u.interrupt(); err != nil {
					return err
				}
			} else {
				return nil
			}
		case now := <-timer.C:
			for _, p := range u.pending {
				if now.After(p.deadline) {
					return fmt.Errorf("Codex app-server timed out during %s", p.method)
				}
			}
		}
	}
}
