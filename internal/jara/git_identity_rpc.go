package jara

import (
	"encoding/json"
	"fmt"
	"io"
)

func gitIdentityRPC(operation string, input io.Reader, output io.Writer) error {
	switch operation {
	case "git-identity-show":
		identity, err := readGitIdentity()
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(identity)
	case "git-identity-set":
		raw, err := io.ReadAll(io.LimitReader(input, 8193))
		if err != nil {
			return err
		}
		if len(raw) > 8192 {
			return fmt.Errorf("Git identity exceeds 8 KiB")
		}
		var identity gitIdentity
		if err := json.Unmarshal(raw, &identity); err != nil {
			return fmt.Errorf("invalid Git identity: %w", err)
		}
		return setGitIdentity(identity)
	case "git-identity-clear":
		return clearGitIdentity()
	default:
		return fmt.Errorf("unknown Git identity operation")
	}
}
