package agentrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"krystof.liskovec/jara/internal/protocol"
	"krystof.liskovec/jara/internal/textutil"
)

// A /btw side question sees the conversation so far but never becomes part of
// it: Codex answers in an ephemeral read-only fork, Claude Code in a forked
// session that is not saved. Either way the main turn keeps running.
const (
	BtwTimeout      = 3 * time.Minute
	btwInstructions = "The user is asking a quick side question (/btw) while the main conversation continues. " +
		"Answer it briefly and directly from the conversation so far. Do not run commands, use tools, or change files; " +
		"if answering would need that, say what you would check instead."
)

type btwFork struct {
	uiID   json.RawMessage
	answer strings.Builder
	timer  *time.Timer
}

type btwRequest struct {
	ThreadID string `json:"threadId"`
	Question string `json:"question"`
}

func (r *agentRouter) askBtw(id json.RawMessage, params json.RawMessage) error {
	var request btwRequest
	_ = json.Unmarshal(params, &request)
	request.Question = strings.TrimSpace(request.Question)
	if request.ThreadID == "" || request.Question == "" {
		r.fail(id, "a side question needs an open conversation and a question")
		return nil
	}
	codexThread := request.ThreadID
	if thread := r.threads[request.ThreadID]; thread != nil {
		segment := thread.Current()
		if segment.Agent == protocol.AgentClaude {
			r.askClaudeBtw(id, thread, segment, request.Question)
			return nil
		}
		codexThread = segment.BackendID
	}
	forkParams, _ := json.Marshal(map[string]any{
		"threadId": codexThread, "ephemeral": true, "excludeTurns": true,
		"sandbox": "read-only", "approvalPolicy": "never", "developerInstructions": btwInstructions,
	})
	r.btwForking++
	return r.callCodex(codexCall{method: "thread/fork", callback: func(result, failure json.RawMessage) {
		r.btwForking--
		var forked struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
		}
		if failure != nil || json.Unmarshal(result, &forked) != nil || forked.Thread.ID == "" {
			r.fail(id, "could not open a side conversation: "+rpcErrorText(failure))
			return
		}
		fork := &btwFork{uiID: id}
		if r.btw == nil {
			r.btw = map[string]*btwFork{}
		}
		r.btw[forked.Thread.ID] = fork
		forkID := forked.Thread.ID
		fork.timer = time.AfterFunc(BtwTimeout, func() { r.events <- routerEvent{source: "btw", btwThread: forkID} })
		turnParams, _ := json.Marshal(map[string]any{"threadId": forkID, "input": []any{map[string]any{"type": "text", "text": request.Question}}})
		_ = r.callCodex(codexCall{method: "turn/start", callback: func(_, failure json.RawMessage) {
			if failure != nil {
				r.finishBtw(forkID, "", "could not ask the side question: "+rpcErrorText(failure))
			}
		}}, turnParams)
	}}, forkParams)
}

// captureBtw consumes Codex messages that belong to a side-question fork so
// they never reach the main transcript.
func (r *agentRouter) captureBtw(m rpcMessage) bool {
	var params struct {
		ThreadID string `json:"threadId"`
		Thread   struct {
			ID        string `json:"id"`
			Ephemeral bool   `json:"ephemeral"`
		} `json:"thread"`
		Item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"item"`
		Turn struct {
			Status string `json:"status"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	if json.Unmarshal(m.Params, &params) != nil {
		return false
	}
	if m.Method == "thread/started" && params.Thread.Ephemeral && r.btwForking > 0 {
		return true // the fork announces itself before its ID is known
	}
	fork := r.btw[textutil.FirstNonEmpty(params.ThreadID, params.Thread.ID)]
	if fork == nil {
		return false
	}
	threadID := textutil.FirstNonEmpty(params.ThreadID, params.Thread.ID)
	switch {
	case len(m.ID) > 0:
		// The fork is read-only and never asks; decline anything it requests.
		_ = r.codexIn.Encode(map[string]any{"id": m.ID, "error": map[string]any{"code": -32000, "message": "side questions cannot request actions"}})
	case m.Method == "item/completed" && params.Item.Type == "agentMessage":
		if fork.answer.Len() > 0 {
			fork.answer.WriteString("\n\n")
		}
		fork.answer.WriteString(params.Item.Text)
	case m.Method == "turn/completed":
		failure := ""
		if params.Turn.Error != nil {
			failure = params.Turn.Error.Message
		} else if params.Turn.Status != "completed" && fork.answer.Len() == 0 {
			failure = "the side question ended without an answer (" + params.Turn.Status + ")"
		}
		r.finishBtw(threadID, fork.answer.String(), failure)
	}
	return true
}

// finishBtw answers the UI once and releases the ephemeral fork.
func (r *agentRouter) finishBtw(forkID, answer, failure string) {
	fork := r.btw[forkID]
	if fork == nil {
		return
	}
	delete(r.btw, forkID)
	if fork.timer != nil {
		fork.timer.Stop()
	}
	params, _ := json.Marshal(map[string]any{"threadId": forkID})
	_ = r.callCodex(codexCall{method: "thread/unsubscribe", callback: func(json.RawMessage, json.RawMessage) {}}, params)
	if failure != "" {
		r.fail(fork.uiID, failure)
		return
	}
	r.reply(fork.uiID, map[string]any{"answer": answer})
}

// askClaudeBtw asks a forked, unsaved Claude Code session without tools.
func (r *agentRouter) askClaudeBtw(id json.RawMessage, thread *Thread, segment ThreadSegment, question string) {
	args := []string{"-p", "--output-format", "json", "--no-session-persistence", "--tools", "", "--append-system-prompt", btwInstructions}
	args = append(args, r.claudeArgs...)
	if segment.Started {
		args = append(args, "--resume", segment.BackendID, "--fork-session")
	}
	if model := protocol.ClaudeCLIModel(thread.Model); model != "" {
		args = append(args, "--model", model)
	}
	cwd := thread.CWD
	go func() {
		answer, err := runClaudeBtw(cwd, args, question)
		if err != nil {
			r.fail(id, err.Error())
			return
		}
		r.reply(id, map[string]any{"answer": answer})
	}()
}

func runClaudeBtw(cwd string, args []string, question string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), BtwTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, protocol.ClaudeExecutable(), args...)
	cmd.Dir = cwd
	cmd.Env = claudeEnv(os.Environ())
	cmd.Stdin = strings.NewReader(question)
	stderr := &tailBuffer{}
	cmd.Stderr = stderr
	output, err := cmd.Output()
	var result struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
	}
	if parseErr := json.Unmarshal(bytes.TrimSpace(output), &result); parseErr != nil {
		detail := textutil.FirstNonEmpty(stderr.String(), fmt.Sprint(err), parseErr.Error())
		return "", fmt.Errorf("Claude Code could not answer the side question: %s", detail)
	}
	if result.IsError {
		return "", fmt.Errorf("Claude Code could not answer the side question: %s", result.Result)
	}
	return result.Result, nil
}

func rpcErrorText(failure json.RawMessage) string {
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(failure, &detail) == nil && detail.Message != "" {
		return detail.Message
	}
	return "no response"
}
