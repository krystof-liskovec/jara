package jara

import (
	"reflect"
	"strings"
	"testing"
)

func TestConfiguredCodexArgsPreserveTokens(t *testing.T) {
	t.Setenv("JARA_CODEX_ARGS", `["--model","env-model","-c","key=\"space $HOME $(command) `+"`id`"+`\""]`)
	extra := []string{"--config", "another=\"two words\""}
	got, err := configuredCodexArgs(extra)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "env-model", "-c", "key=\"space $HOME $(command) `id`\"", "--config", "another=\"two words\""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	if !reflect.DeepEqual(codexCommand(true, got), append([]string{"/usr/local/bin/codex", "resume"}, want...)) {
		t.Fatal("resume argument order")
	}
}

func TestCodexArgsValidation(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `[1]`, `"--yolo"`, `["\u0000"]`, "[", strings.Repeat(" ", 65537)} {
		if _, err := parseCodexArgs([]byte(raw)); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	t.Setenv("JARA_CODEX_ARGS", "[]")
	args, err := configuredCodexArgs(nil)
	if err != nil || len(args) != 0 {
		t.Fatalf("default arguments %q: %v", args, err)
	}
	if err := validateCodexArgs(make([]string, 129)); err == nil {
		t.Fatal("accepted too many args")
	}
	if err := validateCodexArgs([]string{strings.Repeat("x", 65537)}); err == nil {
		t.Fatal("accepted oversized args")
	}
}

func TestCodexArgsPassthrough(t *testing.T) {
	args, extra := splitCodexArgs([]string{"--remote", "work", "--", "--model", "some-model", "--", "prompt"})
	if !reflect.DeepEqual(args, []string{"--remote", "work"}) || !reflect.DeepEqual(extra, []string{"--model", "some-model", "--", "prompt"}) {
		t.Fatalf("%q / %q", args, extra)
	}
}
