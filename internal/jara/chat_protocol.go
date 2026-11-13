package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/protocol"
)

// appMessage preserves request IDs verbatim: server requests may use string IDs.
type appMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}
type appConnection struct {
	cmd      *exec.Cmd
	input    io.WriteCloser
	reader   io.Reader
	messages chan appMessage
	errors   chan error
	stop     chan struct{}
	encoder  *json.Encoder
	sequence int
	closed   sync.Once
}

func startChatServer(binary string, args []string, stderr io.Writer) (*appConnection, error) {
	return startChatServerCommand(exec.Command(binary, args...), stderr)
}

func startChatServerCommand(cmd *exec.Cmd, stderr io.Writer) (*appConnection, error) {
	cmd.Stderr = stderr
	isolateChatProcess(cmd)
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		input.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		input.Close()
		return nil, err
	}
	return newAppConnection(cmd, input, output), nil
}

func startChatConnection(connection io.ReadWriteCloser) *appConnection {
	return newAppConnection(nil, connection, connection)
}

func newAppConnection(cmd *exec.Cmd, input io.WriteCloser, output io.Reader) *appConnection {
	c := &appConnection{cmd: cmd, input: input, reader: output, encoder: json.NewEncoder(input), messages: make(chan appMessage, 32), errors: make(chan error, 1), stop: make(chan struct{})}
	go func() {
		scanner := protocol.NewLineScanner(c.reader)
		for scanner.Scan() {
			var m appMessage
			if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
				c.errors <- fmt.Errorf("invalid app-server message: %w", err)
				return
			}
			select {
			case c.messages <- m:
			case <-c.stop:
				return
			}
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		c.errors <- err
	}()
	return c
}

// close is idempotent: a reconnecting interface closes connections it replaced.
func (c *appConnection) close() {
	c.closed.Do(c.closeOnce)
}

func (c *appConnection) closeOnce() {
	close(c.stop)
	_ = c.input.Close()
	if c.cmd == nil {
		return
	}
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}
func (c *appConnection) request(method string, params any) (string, error) {
	c.sequence++
	id := fmt.Sprint(c.sequence)
	return id, c.encoder.Encode(map[string]any{"id": c.sequence, "method": method, "params": params})
}
func (c *appConnection) reply(id json.RawMessage, result any) error {
	return c.encoder.Encode(map[string]any{"id": id, "result": result})
}
func (c *appConnection) unsupported(m appMessage) error {
	return c.encoder.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32601, "message": "This Jara interface does not support " + m.Method}})
}
