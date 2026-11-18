package jara

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
	"krystof.liskovec/jara/internal/protocol"
)

// Idle compaction: when a large conversation has been idle for almost the
// lifetime of the provider's prompt cache, the session daemon compacts it
// while the cache is still warm. The compaction then reads the context at the
// cached rate, and the next prompt starts from a short summary instead of
// re-reading the whole uncached conversation. The daemon keeps running while
// windows are detached, which is when this matters most.

const (
	// The default delays stay inside each agent's usual cache lifetime.
	// OpenAI caches GPT-5.6 and later prompts for at least 30 minutes, and
	// GPT-5.5 and earlier models with extended retention typically for about
	// 30 minutes (in-memory retention, the default under Zero Data Retention,
	// lasts 5 to 10 minutes). Claude Code subscriptions cache prompts for an
	// hour. /compact idle DURATION overrides the delay for the conversation's
	// agent.
	chatIdleCompactCodexAfter  = 25 * time.Minute
	chatIdleCompactClaudeAfter = 55 * time.Minute
	chatIdleCompactMinAfter    = time.Minute
	chatIdleCompactMaxAfter    = 24 * time.Hour
	chatIdleCompactPercent     = 50      // of the context window in use
	chatIdleCompactMinTokens   = 100_000 // when the window is not reported
)

type chatIdleCompactConfig struct {
	Disabled bool              `json:"disabled,omitempty"`
	After    map[string]string `json:"after,omitempty"` // Go durations by agent
}

// after is the idle delay for an agent; an unknown agent is treated as Codex,
// the default runtime.
func (c chatIdleCompactConfig) after(agent string) time.Duration {
	agent = chatIdleCompactAgent(agent)
	if after, err := parseChatIdleCompactAfter(c.After[agent]); err == nil && after > 0 {
		return after
	}
	if agent == protocol.AgentClaude {
		return chatIdleCompactClaudeAfter
	}
	return chatIdleCompactCodexAfter
}

func chatIdleCompactAgent(agent string) string {
	if agent == protocol.AgentClaude {
		return agent
	}
	return protocol.AgentCodex
}

func parseChatIdleCompactAfter(raw string) (time.Duration, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	after, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || after < chatIdleCompactMinAfter || after > chatIdleCompactMaxAfter {
		return 0, fmt.Errorf("use a duration from %s to %s, such as 5m or 55m", chatIdleCompactMinAfter, chatIdleCompactMaxAfter)
	}
	return after, nil
}

func chatIdleCompactConfigPath() string {
	return filepath.Join(fsutil.DataRoot(), "idle-compaction.json")
}

func loadChatIdleCompactConfig() chatIdleCompactConfig {
	var config chatIdleCompactConfig
	if data, err := os.ReadFile(chatIdleCompactConfigPath()); err == nil {
		_ = json.Unmarshal(data, &config)
	}
	return config
}

func chatIdleCompactRequest(method string) bool {
	return strings.HasPrefix(method, "jara/idleCompaction/")
}

// handleChatIdleCompactRequest serves /compact idle: get, and set with
// enabled and an optional delay for the conversation's agent.
func handleChatIdleCompactRequest(method string, params json.RawMessage, agent string) (any, error) {
	agent = chatIdleCompactAgent(agent)
	config := loadChatIdleCompactConfig()
	switch method {
	case "jara/idleCompaction/get":
	case "jara/idleCompaction/set":
		var request struct {
			Enabled *bool  `json:"enabled"`
			After   string `json:"after"`
		}
		_ = json.Unmarshal(params, &request)
		if request.Enabled != nil {
			config.Disabled = !*request.Enabled
		}
		if request.After != "" {
			after, err := parseChatIdleCompactAfter(request.After)
			if err != nil {
				return nil, err
			}
			if config.After == nil {
				config.After = map[string]string{}
			}
			config.After[agent] = after.String()
		}
		if err := fsutil.WriteJSON(chatIdleCompactConfigPath(), config); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown idle compaction request %s", method)
	}
	return map[string]any{"enabled": !config.Disabled, "agent": agent, "after": config.after(agent).String()}, nil
}

// chatIdleCompactor watches the conversation's traffic in the daemon and
// starts one compaction per idle period.
type chatIdleCompactor struct {
	mu         sync.Mutex
	thread     string // the conversation the usage belongs to
	used       int64
	window     int64
	idleSince  time.Time // zero while a turn runs or after a compaction
	requestID  string    // the compaction request awaiting its response
	compacting bool      // the compaction turn has not completed yet
	sequence   int
	now        func() time.Time
	config     func() chatIdleCompactConfig
	// begin runs before the request is written, with the input lock held:
	// the daemon marks the turn active, tracks the request, and broadcasts
	// the notice so prompts sent meanwhile wait for the compaction.
	begin func(requestID string, notice []byte)
}

func newChatIdleCompactor() *chatIdleCompactor {
	return &chatIdleCompactor{now: time.Now, config: loadChatIdleCompactConfig}
}

// running reports whether a compaction this daemon started is in flight, so
// scheduled and crunch turns wait for it.
func (c *chatIdleCompactor) running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestID != "" || c.compacting
}

// observe follows the app-server messages of the open conversation.
func (c *chatIdleCompactor) observe(message appMessage, conversation string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conversation != c.thread {
		c.thread, c.used, c.window, c.idleSince = conversation, 0, 0, time.Time{}
	}
	if len(message.ID) > 0 && message.Method == "" {
		if c.requestID != "" && string(message.ID) == fmt.Sprintf("%q", c.requestID) {
			c.requestID = ""
			c.compacting = message.Error == nil
			c.idleSince = time.Time{}
		}
		return
	}
	var params struct {
		ThreadID   string `json:"threadId"`
		TokenUsage struct {
			Last struct {
				TotalTokens int64 `json:"totalTokens"`
			} `json:"last"`
			ModelContextWindow int64 `json:"modelContextWindow"`
		} `json:"tokenUsage"`
	}
	if json.Unmarshal(message.Params, &params) != nil || params.ThreadID != "" && params.ThreadID != conversation {
		return
	}
	switch message.Method {
	case "thread/tokenUsage/updated":
		c.used = params.TokenUsage.Last.TotalTokens
		if params.TokenUsage.ModelContextWindow > 0 {
			c.window = params.TokenUsage.ModelContextWindow
		}
	case "thread/compacted":
		c.used = 0
	case "turn/started":
		c.idleSince = time.Time{}
	case "turn/completed":
		if c.compacting {
			// The usage reported during compaction is the old context's.
			c.compacting, c.used = false, 0
			return
		}
		c.idleSince = c.now()
	}
}

func (c *chatIdleCompactor) largeLocked() bool {
	if c.window > 0 {
		return c.used*100 >= c.window*chatIdleCompactPercent
	}
	return c.used >= chatIdleCompactMinTokens
}

// tick starts a compaction once the conversation has been idle long enough
// for its agent. busy reports turns, loading, and other daemon work that must
// not overlap.
func (c *chatIdleCompactor) tick(input io.Writer, inputMu *sync.Mutex, agent string, busy func() bool) error {
	c.mu.Lock()
	if c.thread == "" || c.requestID != "" || c.compacting || c.idleSince.IsZero() || !c.largeLocked() {
		c.mu.Unlock()
		return nil
	}
	config := c.config()
	if config.Disabled || c.now().Sub(c.idleSince) < config.after(agent) {
		c.mu.Unlock()
		return nil
	}
	c.sequence++
	requestID := fmt.Sprintf("jara-idle-compact-%d", c.sequence)
	thread := c.thread
	c.mu.Unlock()

	message, err := json.Marshal(map[string]any{
		"id": requestID, "method": "thread/compact/start", "params": map[string]string{"threadId": thread},
	})
	if err != nil {
		return err
	}
	notice, err := json.Marshal(map[string]any{
		"method": "jara/idleCompaction/started", "params": map[string]string{"threadId": thread},
	})
	if err != nil {
		return err
	}
	inputMu.Lock()
	defer inputMu.Unlock()
	if busy() {
		return nil // retried on the next tick
	}
	c.mu.Lock()
	if c.thread != thread || c.idleSince.IsZero() {
		c.mu.Unlock()
		return nil // a turn started meanwhile
	}
	c.requestID, c.idleSince = requestID, time.Time{}
	c.mu.Unlock()
	if c.begin != nil {
		c.begin(requestID, notice)
	}
	if _, err := input.Write(append(message, '\n')); err != nil {
		c.mu.Lock()
		c.requestID = ""
		c.mu.Unlock()
		return err
	}
	return nil
}
