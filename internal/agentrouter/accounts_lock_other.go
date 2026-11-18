//go:build !unix

package agentrouter

func lockAccounts(string) func() { return func() {} }
