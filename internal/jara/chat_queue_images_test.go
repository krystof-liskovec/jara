package jara

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var testChatPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

type queueImageWireRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Prompt string `json:"prompt"`
		Images []string
		Input  []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			URL  string `json:"url"`
		} `json:"input"`
	} `json:"params"`
}

func decodeQueueImageRequests(t *testing.T, wire *bytes.Buffer) []queueImageWireRequest {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(wire.Bytes()))
	var requests []queueImageWireRequest
	for {
		var request queueImageWireRequest
		if err := decoder.Decode(&request); err != nil {
			return requests
		}
		requests = append(requests, request)
	}
}

func TestPersistentQueueSendsImagesToTheSession(t *testing.T) {
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.persistentSession, u.busy, u.turn = true, true, "turn-1"
	placeholder, err := u.attachChatImage(testChatPNG)
	if err != nil {
		t.Fatal(err)
	}
	if err = u.queuePrompt(u.thread, "look at "+placeholder); err != nil {
		t.Fatal(err)
	}
	requests := decodeQueueImageRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "jara/queue/add" || len(requests[0].Params.Images) != 1 ||
		!strings.HasPrefix(requests[0].Params.Images[0], "data:image/png;base64,") || len(u.images) != 0 {
		t.Fatalf("queue request = %+v, images left = %d", requests, len(u.images))
	}
}

func TestLocalQueueSendsImagesWithTheTurn(t *testing.T) {
	var out, wire bytes.Buffer
	u := newChatCommandTestUI(&out, &wire)
	u.busy, u.turn = true, "turn-1"
	placeholder, _ := u.attachChatImage(testChatPNG)
	if err := u.queuePrompt(u.thread, "queued "+placeholder); err != nil {
		t.Fatal(err)
	}
	// An image attached later reuses the number; it must not be confused.
	if _, err := u.attachChatImage(testChatPNG); err != nil {
		t.Fatal(err)
	}
	u.busy, u.turn = false, ""
	if started, err := u.dispatchQueuedPrompt(); err != nil || !started {
		t.Fatalf("dispatch = %v %v", started, err)
	}
	requests := decodeQueueImageRequests(t, &wire)
	if len(requests) != 1 || requests[0].Method != "turn/start" || len(requests[0].Params.Input) != 2 || requests[0].Params.Input[1].Type != "image" {
		t.Fatalf("turn request = %+v", requests)
	}
	if len(u.images) != 1 {
		t.Fatalf("the later image was taken: %d left", len(u.images))
	}

	// A refused turn/start puts the prompt back with its images.
	_ = u.response(appMessage{ID: requests[0].ID, Error: &struct {
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}{Message: "busy"}})
	if len(u.queuedPrompts) != 1 || len(u.queuedPrompts[0].images) != 1 {
		t.Fatalf("requeued prompt = %+v", u.queuedPrompts)
	}
}

func TestSessionQueueStartsTurnsWithImages(t *testing.T) {
	m, err := newChatScheduleManager(filepath.Join(t.TempDir(), "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	image := "data:image/png;base64,iVBORw0KGgo="
	add := func(images ...string) error {
		raw, _ := json.Marshal(map[string]any{"threadId": "th-1", "prompt": "see [Image #1]", "images": images})
		_, err := m.handleQueueRequest("jara/queue/add", raw, "th-1")
		return err
	}
	if add("data:text/html;base64,PGI+") == nil || add("https://example.com/a.png") == nil {
		t.Fatal("accepted something that is not an image")
	}
	if err = add(image); err != nil {
		t.Fatal(err)
	}
	if notice := string(chatQueueNotification(m.queuedPrompts())); strings.Contains(notice, "base64") {
		t.Fatalf("queue notices carry image data: %s", notice)
	}
	var input bytes.Buffer
	var mu sync.Mutex
	if err = m.startDue(&input, &mu, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.String(), `{"type":"image","url":"`+image+`"}`) {
		t.Fatalf("turn/start lacks the image: %s", input.String())
	}
	m.finishFailed(m.requestID, "done")
	data, _ := os.ReadFile(m.path)
	if strings.Contains(string(data), "base64") {
		t.Fatalf("finished prompts keep their images: %s", data)
	}
}
