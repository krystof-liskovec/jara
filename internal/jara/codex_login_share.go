package jara

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"krystof.liskovec/jara/internal/agentrouter"
	"strings"
	"sync"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// Codex on every host: an OpenAI API key can be saved everywhere at once; a
// ChatGPT account signs in on each host in turn with its own device code, so
// each host keeps an independent login that renews on its own. Both talk to a
// short-lived Codex app-server on each host (over SSH for remotes), which
// saves the sign-in where that host's Codex reads it; Codex sessions already
// running there pick it up.

type codexPlace struct {
	name     string // "" is this device
	start    func(context.Context) (*codexOneShot, error)
	remember func() error // keeps the account in use in the place's pool
}

// codexPlaces lists this device, when it has Codex, then every remote.
func codexPlaces(c Config) []codexPlace {
	var places []codexPlace
	for _, name := range chatRemotePlaces(c) {
		if name == "" {
			binary, ok := installedAgent(protocol.CodexExecutable(), "codex")
			if ok {
				places = append(places, codexPlace{start: func(ctx context.Context) (*codexOneShot, error) { return startCodexOneShot(ctx, binary) },
					remember: func() error { agentrouter.RememberAccounts(); return nil }})
			}
			continue
		}
		remote := c.Remotes[name]
		places = append(places, codexPlace{name: name, start: func(ctx context.Context) (*codexOneShot, error) { return startRemoteCodexOneShot(ctx, remote) },
			remember: func() error { return rpc(remote, Request{Op: "accounts-remember"}, nil, nil) }})
	}
	return places
}

func (p codexPlace) title() string { return textOr(p.name, "This device") }

func validateOpenAIKey(key string) error {
	if len(key) < 20 || strings.ContainsAny(key, " \t\r\n\x00") {
		return errors.New("expected an OpenAI API key such as sk-proj-…, on one line")
	}
	return nil
}

// shareCodexAPIKey saves key for Codex on every place at once.
func shareCodexAPIKey(c Config, key string) string {
	key = strings.TrimSpace(key)
	if err := validateOpenAIKey(key); err != nil {
		return "Error: " + err.Error() + "\n"
	}
	places := codexPlaces(c)
	if len(places) == 0 {
		return "Error: Codex is not installed here and no remotes are configured.\n"
	}
	results := make([]string, len(places))
	var wait sync.WaitGroup
	for i, place := range places {
		wait.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			err := codexOnPlace(ctx, place, func(server *codexOneShot) error {
				return server.request("account/login/start", map[string]string{"type": "apiKey", "apiKey": key}, nil)
			})
			results[i] = place.title() + ": " + savedOrNot(err)
		})
	}
	wait.Wait()
	text := strings.Join(results, "\n") + "\n"
	if strings.Contains(text, "not saved") {
		text += "Run it again to reach the hosts that were not saved.\n"
	}
	return text
}

func codexOnPlace(ctx context.Context, place codexPlace, work func(*codexOneShot) error) error {
	server, err := place.start(ctx)
	if err != nil {
		return codexPlaceError(err)
	}
	defer server.close()
	return codexPlaceError(work(server))
}

func codexPlaceError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return errors.New("cancelled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("timed out")
	}
	return errors.New(strings.TrimPrefix(err.Error(), "Codex: "))
}

// codexAccount describes the ChatGPT account a place's Codex uses, or "".
func codexAccount(server *codexOneShot) string {
	var result struct {
		Account *struct {
			Type     string  `json:"type"`
			Email    *string `json:"email"`
			PlanType string  `json:"planType"`
		} `json:"account"`
	}
	if server.request("account/read", map[string]any{}, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" {
		return ""
	}
	return chatSignedInAs(result.Account.Email, result.Account.PlanType)
}

// codexDeviceLoginEverywhere signs a ChatGPT account in on each place in
// turn: say shows each place's code, and it waits until that one is used.
// Places already signed in to an account are left as they are, unless add
// signs a further account in for each place's pool.
func codexDeviceLoginEverywhere(ctx context.Context, c Config, say func(string), add bool) string {
	places := codexPlaces(c)
	if len(places) == 0 {
		return "Codex is not installed here and no remotes are configured.\n"
	}
	var results []string
	for index, place := range places {
		if ctx.Err() != nil {
			results = append(results, place.title()+": not signed in (cancelled)")
			continue
		}
		step := fmt.Sprintf("%d of %d", index+1, len(places))
		placeCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		result := ""
		err := codexOnPlace(placeCtx, place, func(server *codexOneShot) error {
			if account := codexAccount(server); account != "" && !add {
				result = "already " + account
				return nil
			}
			if add && place.remember() != nil {
				return errors.New("this host runs an older Jara, which would not keep the account in use; update it with jara deployment upgrade")
			}
			var start struct{ UserCode, VerificationURL string }
			if err := server.request("account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &start); err != nil {
				return err
			}
			say("### Codex on " + place.title() + " (" + step + ")\n1. Open " + chatText(start.VerificationURL) +
				"\n2. Enter the code `" + chatText(start.UserCode) + "`\n")
			params, err := server.waitFor("account/login/completed")
			if err != nil {
				return err
			}
			var done struct {
				Success bool    `json:"success"`
				Error   *string `json:"error"`
			}
			_ = json.Unmarshal(params, &done)
			if !done.Success {
				return errors.New(textOr(chatText(stringValue(done.Error)), "the sign-in did not go through"))
			}
			result = textOr(codexAccount(server), "signed in")
			if add {
				_ = place.remember()
			}
			return nil
		})
		cancel()
		if err != nil {
			result = "not signed in (" + err.Error() + ")"
		}
		results = append(results, place.title()+": "+result)
	}
	text := strings.Join(results, "\n") + "\n"
	if strings.Contains(text, "not signed in") {
		text += "Run it again for the hosts that are not signed in; the others are skipped.\n"
	}
	return text
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// codexAuthEverywhere runs jara auth codex --all [--api-key].
func codexAuthEverywhere(c Config, apiKey, keyStdin, add bool) error {
	var report string
	if apiKey {
		key, err := readSecretLine(keyStdin, "OpenAI API key (input hidden): ")
		if err != nil {
			return err
		}
		report = shareCodexAPIKey(c, key)
	} else {
		if add {
			fmt.Println("Adding a Codex account on each host in turn; the accounts there stay in their pools. Ctrl+C stops.")
		} else {
			fmt.Println("Signing Codex in on each host in turn; hosts already signed in are skipped. Ctrl+C stops.")
		}
		report = codexDeviceLoginEverywhere(context.Background(), c, func(step string) {
			fmt.Print("\n" + strings.NewReplacer("### ", "", "`", "").Replace(step))
		}, add)
		fmt.Println()
	}
	fmt.Print(report)
	if strings.Contains(report, "not ") || strings.HasPrefix(report, "Error") {
		return errors.New("some hosts were not signed in")
	}
	return nil
}
