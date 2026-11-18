package agentrouter

// claudeMCPStatus is one MCP server as Claude Code's init message reports it.
type claudeMCPStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"` // connected, failed, needs-auth or pending
}

// mcpStatus answers mcpServerStatus/list for a Claude Code conversation in
// the shape Codex uses. Before the first turn starts Claude Code, it knows
// no servers and says so with started false.
func (s *claudeSession) mcpStatus() map[string]any {
	data := []map[string]any{}
	for _, server := range s.mcpServers {
		data = append(data, map[string]any{"name": server.Name, "runtimeStatus": claudeMCPRuntimeStatus(server.Status)})
	}
	return map[string]any{"data": data, "started": s.cmd != nil}
}

func claudeMCPRuntimeStatus(status string) string {
	switch status {
	case "connected", "failed":
		return status
	case "needs-auth":
		return "authenticationRequired"
	case "pending":
		return "starting"
	}
	return status
}
