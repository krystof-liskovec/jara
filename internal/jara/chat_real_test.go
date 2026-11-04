package jara

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestInstalledCodexAppServerMetadataSmoke(t *testing.T) {
	if os.Getenv("JARA_REAL_CODEX_TEST") != "1" {
		t.Skip("set JARA_REAL_CODEX_TEST=1 to check the installed Codex app-server")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex executable is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	connection, err := startChatServer(binary, []string{"app-server", "--listen", "stdio://"}, io.Discard)
	if err != nil {
		t.Fatalf("could not start installed Codex app-server: %v", err)
	}
	t.Cleanup(connection.close)

	initialize, err := requestInstalledCodex(ctx, connection, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "jara-smoke-test", "title": "Jara smoke test", "version": Version},
	})
	if err != nil {
		t.Fatal(err)
	}
	var initializedResult map[string]json.RawMessage
	if len(initialize.Result) == 0 || json.Unmarshal(initialize.Result, &initializedResult) != nil || initializedResult == nil {
		t.Fatal("installed Codex app-server returned an invalid initialize result")
	}
	if err := connection.encoder.Encode(map[string]string{"method": "initialized"}); err != nil {
		t.Fatalf("could not complete app-server initialization: %v", err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("could not resolve the smoke test working directory")
	}
	threads, err := requestInstalledCodex(ctx, connection, "thread/list", map[string]any{
		"cwd": cwd, "limit": 1, "useStateDbOnly": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInstalledList(threads.Result, cwd, true); err != nil {
		t.Fatal(err)
	}

	models, err := requestInstalledCodex(ctx, connection, "model/list", map[string]any{"limit": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateInstalledList(models.Result, "", false); err != nil {
		t.Fatal(err)
	}
}

func requestInstalledCodex(ctx context.Context, connection *appConnection, method string, params any) (appMessage, error) {
	id, err := connection.request(method, params)
	if err != nil {
		return appMessage{}, fmt.Errorf("could not send %s request to installed Codex app-server", method)
	}
	for {
		select {
		case message, ok := <-connection.messages:
			if !ok {
				return appMessage{}, fmt.Errorf("installed Codex app-server closed during %s", method)
			}
			if bytes.Equal(bytes.TrimSpace(message.ID), []byte(id)) {
				if message.Error != nil {
					return appMessage{}, fmt.Errorf("installed Codex app-server rejected %s", method)
				}
				return message, nil
			}
			if len(message.ID) > 0 && message.Method != "" {
				if err := connection.unsupported(message); err != nil {
					return appMessage{}, fmt.Errorf("could not answer an app-server request during %s", method)
				}
			}
		case <-connection.errors:
			return appMessage{}, fmt.Errorf("installed Codex app-server transport failed during %s", method)
		case <-ctx.Done():
			return appMessage{}, fmt.Errorf("installed Codex app-server timed out during %s", method)
		}
	}
}

func validateInstalledList(result json.RawMessage, cwd string, checkCWD bool) error {
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	if len(result) == 0 || json.Unmarshal(result, &page) != nil || page.Data == nil {
		return fmt.Errorf("installed Codex app-server returned an invalid list result")
	}
	if !checkCWD {
		// An empty model catalog is valid without account access; populated
		// entries must expose the fields Jara's picker consumes.
		for _, item := range page.Data {
			var model struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
			}
			if json.Unmarshal(item, &model) != nil || model.ID == "" || model.Model == "" || model.DisplayName == "" {
				return fmt.Errorf("installed Codex app-server returned an incomplete model entry")
			}
		}
		return nil
	}
	for _, item := range page.Data {
		var thread struct {
			CWD string `json:"cwd"`
		}
		if json.Unmarshal(item, &thread) != nil || thread.CWD != cwd {
			return fmt.Errorf("installed Codex app-server did not honor the exact cwd filter")
		}
	}
	return nil
}
