package jara

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"

	"krystof.liskovec/jara/internal/protocol"
)

// codexOneShot is a Codex app-server started for a few requests, such as
// listing conversations or reading usage limits.
type codexOneShot struct {
	ctx      context.Context
	cmd      *exec.Cmd
	in       io.WriteCloser
	scanner  *protocol.LineScanner
	encoder  *json.Encoder
	sequence int
	stopped  chan struct{}
	once     sync.Once
}

// startCodexOneShot starts binary's app-server and initializes it.
func startCodexOneShot(ctx context.Context, binary string) (*codexOneShot, error) {
	return startCodexOneShotCommand(ctx, exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://"))
}

// startRemoteCodexOneShot runs a remote's Codex app-server over SSH.
func startRemoteCodexOneShot(ctx context.Context, r Remote) (*codexOneShot, error) {
	return startCodexOneShotCommand(ctx, sshCommand(r, false, shellArgs("codex", "app-server", "--listen", "stdio://")))
}

func startCodexOneShotCommand(ctx context.Context, cmd *exec.Cmd) (*codexOneShot, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	stopped := make(chan struct{})
	go func() { // a command made without ctx stops with it too
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
		case <-stopped:
		}
	}()
	s := &codexOneShot{stopped: stopped, ctx: ctx, cmd: cmd, in: in, scanner: protocol.NewLineScanner(out), encoder: json.NewEncoder(in)}
	if err = s.request("initialize", map[string]any{"clientInfo": map[string]string{"name": "jara", "title": "Jara", "version": Version}}, nil); err == nil {
		err = s.encoder.Encode(map[string]any{"method": "initialized"})
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *codexOneShot) close() {
	s.once.Do(func() {
		close(s.stopped)
		s.in.Close()
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	})
}

// waitFor returns the params of the next notification named method.
func (s *codexOneShot) waitFor(method string) (json.RawMessage, error) {
	for s.scanner.Scan() {
		var message struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(s.scanner.Bytes(), &message) == nil && message.Method == method {
			return message.Params, nil
		}
	}
	if s.ctx.Err() != nil {
		return nil, s.ctx.Err()
	}
	return nil, io.ErrUnexpectedEOF
}

// request sends method and decodes its result into result, when not nil.
func (s *codexOneShot) request(method string, params any, result any) error {
	s.sequence++
	if err := s.encoder.Encode(map[string]any{"id": s.sequence, "method": method, "params": params}); err != nil {
		return err
	}
	for s.scanner.Scan() {
		var response struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(s.scanner.Bytes(), &response) != nil || response.ID != s.sequence {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("Codex: %s", response.Error.Message)
		}
		if result != nil {
			return json.Unmarshal(response.Result, result)
		}
		return nil
	}
	if s.ctx.Err() != nil {
		return s.ctx.Err()
	}
	if s.scanner.Err() != nil {
		return s.scanner.Err()
	}
	return io.ErrUnexpectedEOF
}
