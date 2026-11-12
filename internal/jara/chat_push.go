package jara

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/fsutil"
)

// Push notifications reach a phone even when its terminal app is in the
// background or disconnected. The session daemon, which keeps running on the
// host, posts them to a URL set with /notify, such as an ntfy.sh topic
// (https://ntfy.sh/SECRET-TOPIC) or any webhook that accepts a text body.
// Messages name the workspace and the event only, never conversation text.

type chatPushConfig struct {
	URL string `json:"url,omitempty"`
}

type chatPushNotifier struct {
	mu          sync.Mutex
	turnStarted map[string]time.Time
	post        func(target, title, body, priority string) error
}

var chatPush = &chatPushNotifier{turnStarted: map[string]time.Time{}, post: postChatPush}

func chatPushConfigPath() string { return filepath.Join(fsutil.DataRoot(), "notify.json") }

func loadChatPushConfig() chatPushConfig {
	var config chatPushConfig
	if data, err := os.ReadFile(chatPushConfigPath()); err == nil {
		_ = json.Unmarshal(data, &config)
	}
	return config
}

func validChatPushURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return fmt.Errorf("use an http(s) URL, such as https://ntfy.sh/your-secret-topic")
	}
	return nil
}

// observe turns app-server traffic into notifications: a turn that ran for a
// while finished, or the agent waits for an approval or an answer.
func (n *chatPushNotifier) observe(message appMessage, workspace string) {
	if message.Method == "" {
		return
	}
	var params struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(message.Params, &params)
	n.mu.Lock()
	defer n.mu.Unlock()
	switch message.Method {
	case "turn/started":
		n.turnStarted[params.Turn.ID] = time.Now()
	case "turn/completed":
		started, known := n.turnStarted[params.Turn.ID]
		delete(n.turnStarted, params.Turn.ID)
		if !known || time.Since(started) < chatAlertAfter || params.Turn.Status == "interrupted" {
			return
		}
		if params.Turn.Status == "completed" {
			n.sendLocked(workspace, "Turn finished", "default")
		} else {
			n.sendLocked(workspace, "Turn ended: "+params.Turn.Status, "default")
		}
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		n.sendLocked(workspace, "Approval needed", "high")
	case "item/tool/requestUserInput":
		n.sendLocked(workspace, "A question is waiting for your answer", "high")
	}
}

func (n *chatPushNotifier) sendLocked(workspace, body, priority string) {
	config := loadChatPushConfig()
	if config.URL == "" {
		return
	}
	title := "Jara · " + strings.TrimSpace(chatText(workspace))
	post := n.post
	go func() { _ = post(config.URL, title, body, priority) }()
}

func postChatPush(target, title, body, priority string) error {
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewBufferString(body))
	if err != nil {
		return err
	}
	// ntfy reads these headers; other webhooks get the text body.
	request.Header.Set("Title", title)
	request.Header.Set("Priority", priority)
	request.Header.Set("Tags", "robot")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		return fmt.Errorf("the notification service answered %s", response.Status)
	}
	return nil
}

func chatNotifyRequest(method string) bool { return strings.HasPrefix(method, "jara/notify/") }

// handleChatNotifyRequest serves /notify for the daemon: get, set (an empty
// URL turns notifications off), and test.
func handleChatNotifyRequest(method string, params json.RawMessage, workspace string) (any, error) {
	var request struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(params, &request)
	config := loadChatPushConfig()
	switch method {
	case "jara/notify/get":
	case "jara/notify/set":
		request.URL = strings.TrimSpace(request.URL)
		if request.URL != "" {
			if err := validChatPushURL(request.URL); err != nil {
				return nil, err
			}
		}
		config.URL = request.URL
		if err := fsutil.WriteJSON(chatPushConfigPath(), config); err != nil {
			return nil, err
		}
	case "jara/notify/test":
		if config.URL == "" {
			return nil, fmt.Errorf("no notification URL is set; use /notify URL first")
		}
		if err := chatPush.post(config.URL, "Jara · "+workspace, "Test notification from Jara", "default"); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown notification request %s", method)
	}
	return map[string]any{"url": config.URL}, nil
}
