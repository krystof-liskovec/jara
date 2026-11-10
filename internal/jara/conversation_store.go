package jara

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

// Conversation is independent of terminal transport and can support more agents.
type Conversation struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Preview string `json:"preview"`
	CWD     string `json:"cwd"`
	Updated int64  `json:"updatedAt"`
	Created int64  `json:"createdAt"`
	Agent   string `json:"-"`
	Status  struct {
		Type string `json:"type"`
	} `json:"status"`
}

type conversationProvider interface {
	Name() string
	ResumeCommand(string, string, []string) []string
	List(context.Context) ([]Conversation, error)
}
type codexConversations struct{ binary string }

func (codexConversations) Name() string { return "Codex" }
func (codexConversations) ResumeCommand(id, cwd string, args []string) []string {
	command := []string{"/usr/local/bin/codex", "resume", id, "--include-non-interactive"}
	if cwd != "" {
		command = append(command, "-C", cwd)
	}
	return append(command, args...)
}
func conversationProviders() []conversationProvider {
	return []conversationProvider{codexConversations{}, routedConversations{}}
}
func resumeConversationCommand(agent, id, cwd string, args []string) ([]string, error) {
	if thread := loadRoutedThread(id); thread != nil {
		return thread.resumeCommand(), nil
	}
	for _, p := range conversationProviders() {
		if p.Name() == agent || agent == "" {
			return p.ResumeCommand(id, cwd, args), nil
		}
	}
	return nil, fmt.Errorf("agent %q is unavailable", agent)
}

// Use Codex's documented metadata API rather than parsing private rollout files.
func (provider codexConversations) List(ctx context.Context) ([]Conversation, error) {
	binary := provider.binary
	if binary == "" {
		binary = "/usr/local/bin/codex"
	}
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
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
	defer func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 4096), 8<<20)
	encoder := json.NewEncoder(in)
	sequence := 0
	request := func(method string, params any, result any) error {
		sequence++
		if err := encoder.Encode(map[string]any{"id": sequence, "method": method, "params": params}); err != nil {
			return err
		}
		for scanner.Scan() {
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &response) != nil || response.ID != sequence {
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
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if scanner.Err() != nil {
			return scanner.Err()
		}
		return io.ErrUnexpectedEOF
	}
	if err = request("initialize", map[string]any{"clientInfo": map[string]string{"name": "jara", "title": "Jara", "version": Version}}, nil); err != nil {
		return nil, err
	}
	if err = encoder.Encode(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	var all []Conversation
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{"limit": 100, "sortKey": "updated_at", "sourceKinds": []string{"cli", "vscode", "exec", "appServer"}}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var page struct {
			Data []Conversation `json:"data"`
			Next *string        `json:"nextCursor"`
		}
		if err = request("thread/list", params, &page); err != nil {
			return all, err
		}
		for _, c := range page.Data {
			if !seen[c.ID] {
				c.Agent = "Codex"
				all = append(all, c)
				seen[c.ID] = true
			}
		}
		if page.Next == nil || *page.Next == "" {
			break
		}
		if *page.Next == cursor {
			return all, fmt.Errorf("Codex returned a repeated history cursor")
		}
		cursor = *page.Next
	}
	return all, nil
}

func loadConversations() ([]Conversation, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return listConversations(ctx, conversationProviders())
}

func listConversations(ctx context.Context, providers []conversationProvider) ([]Conversation, error) {
	var all []Conversation
	var failures []error
	for _, provider := range providers {
		threads, err := provider.List(ctx)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", provider.Name(), err))
		}
		for i := range threads {
			if threads[i].Agent == "" {
				threads[i].Agent = provider.Name()
			}
		}
		all = append(all, threads...)
	}
	return hideRoutedSegments(all), errors.Join(failures...)
}
