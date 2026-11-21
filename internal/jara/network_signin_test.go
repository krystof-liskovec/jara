package jara

import (
	"bytes"
	"strings"
	"testing"
)

func TestJoinOffersToSignTheAgentsIn(t *testing.T) {
	cases := []struct {
		name           string
		codex, claude  bool
		claudeSignedIn bool
		answers        string
		want           string // the sign-in steps taken, in order
		wantOutput     string
	}{
		{name: "nothing installed", wantOutput: "Neither Codex nor Claude Code is installed"},
		{name: "codex by default", codex: true, answers: "\n", want: "codex"},
		{name: "codex declined", codex: true, answers: "n\n", want: ""},
		{name: "both, claude in the browser", codex: true, claude: true, answers: "\n\n", want: "codex,browser"},
		{name: "claude with a token", claude: true, answers: "t\n", want: "read,token"},
		{name: "claude skipped", claude: true, answers: "s\n", want: ""},
		{name: "claude already signed in", claude: true, claudeSignedIn: true, want: "", wantOutput: "already signed in"},
	}
	for _, tc := range cases {
		var steps []string
		step := func(name string) { steps = append(steps, name) }
		a := agentSignIn{
			codexInstalled: tc.codex, claudeInstalled: tc.claude,
			claudeSignedIn:  func() bool { return tc.claudeSignedIn },
			codexEverywhere: func() error { step("codex"); return nil },
			claudeBrowser:   func() error { step("browser"); return nil },
			claudeToken:     func([]byte) error { step("token"); return nil },
			readToken:       func() ([]byte, error) { step("read"); return []byte("sk-ant-oat-x"), nil },
		}
		var out bytes.Buffer
		offerAgentSignIn(strings.NewReader(tc.answers), &out, a)
		if got := strings.Join(steps, ","); got != tc.want {
			t.Errorf("%s: steps %q, want %q\n%s", tc.name, got, tc.want, out.String())
		}
		if !strings.Contains(out.String(), tc.wantOutput) {
			t.Errorf("%s: output lacks %q:\n%s", tc.name, tc.wantOutput, out.String())
		}
	}
}
