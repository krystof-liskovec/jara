package jara

import (
	"reflect"
	"strings"
	"testing"
)

func TestChatRuntimeMetadataAndCommand(t *testing.T) {
	provider, err := resolveChatRuntime("")
	if err != nil {
		t.Fatal(err)
	}
	if provider.name != "codex" || provider.authOwner != "Codex CLI" || provider.protocol != "Codex app-server" {
		t.Fatalf("unexpected runtime metadata: %+v", provider)
	}
	for _, option := range []string{"model", "sandbox_mode", "approval_policy", "web_search", "configuration overrides", "feature toggles"} {
		if !provider.supportsConfiguration(option) {
			t.Errorf("configuration option %q not declared", option)
		}
	}
	binary, args, err := provider.command([]string{"--model", "example"})
	if err != nil {
		t.Fatal(err)
	}
	if binary != "/usr/local/bin/codex" {
		t.Fatalf("unexpected executable: %q", binary)
	}
	want := []string{"app-server", "--listen", "stdio://", "-c", `model="example"`}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args: %q, want %q", args, want)
	}
}

func TestChatRuntimeRejectsUnimplementedProviders(t *testing.T) {
	for _, name := range []string{"claude", "anthropic", "unknown"} {
		t.Run(name, func(t *testing.T) {
			_, err := resolveChatRuntime(name)
			if err == nil || !strings.Contains(err.Error(), "unsupported") || !strings.Contains(err.Error(), "codex") {
				t.Fatalf("provider %q should be rejected with supported choice, got %v", name, err)
			}
		})
	}
}

func TestChatRuntimeConfigCapabilitiesGateMappedOptions(t *testing.T) {
	provider := codexChatRuntime
	provider.configurationOptions = []string{"model"}
	if _, err := provider.serverArgs(provider, []string{"--sandbox", "read-only"}); err == nil || !strings.Contains(err.Error(), "sandbox_mode") {
		t.Fatalf("missing config capability should be rejected, got %v", err)
	}
}
