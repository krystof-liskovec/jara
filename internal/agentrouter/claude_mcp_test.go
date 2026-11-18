package agentrouter

import (
	"encoding/json"
	"testing"
)

func TestClaudeMCPStatusUsesCodexShape(t *testing.T) {
	s := &claudeSession{}
	s.handleLine([]byte(`{"type":"system","subtype":"init","mcp_servers":[{"name":"docs","status":"connected"},{"name":"gh","status":"needs-auth"}]}`))
	got, _ := json.Marshal(s.mcpStatus())
	if string(got) != `{"data":[{"name":"docs","runtimeStatus":"connected"},{"name":"gh","runtimeStatus":"authenticationRequired"}],"started":false}` {
		t.Fatalf("%s", got)
	}
}
