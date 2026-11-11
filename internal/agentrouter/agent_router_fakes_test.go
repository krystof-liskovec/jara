package agentrouter

import (
	"io"
	"os"
	"testing"

	"krystof.liskovec/jara/internal/agentfake"
)

// The test binary doubles as the fake agent CLIs; see internal/agentfake.
func TestMain(m *testing.M) {
	agentfake.ServeIfRequested()
	os.Exit(m.Run())
}

func startRouterHarness(t *testing.T, defaultAgent string, claudeArgs ...string) *agentfake.Harness {
	t.Helper()
	return agentfake.StartHarness(t, func(input io.Reader, output io.Writer) error {
		return runAgentRouter(input, output, agentfake.CodexCommand(), defaultAgent, claudeArgs...)
	})
}
