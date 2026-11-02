package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func gitRepositoryOrigin(root string) (string, error) {
	origin, err := gitOutput(root, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("repository needs an origin remote; use --copy instead")
	}
	if err = validateOrigin(origin); err != nil {
		return "", err
	}
	if isSSHGitOrigin(origin) {
		target, err := parseGitCredentialTarget(origin)
		if err != nil {
			return "", err
		}
		origin = target.URL
	}
	return origin, nil
}

func workspaceID(root, origin string) string {
	identity := root
	if origin != "" {
		identity += "\n" + origin
	}
	sum := sha256.Sum256([]byte(identity))
	return "w-" + hex.EncodeToString(sum[:12])
}

func workspaceAssociationKey(remote, root, origin string) string {
	key := remote + "\n" + root
	if origin != "" {
		key += "\n" + origin
	}
	return key
}

func workspaceForOrigin(remote Remote, id, origin string) (Workspace, bool) {
	var existing Workspace
	if err := rpc(remote, Request{Op: "info", ID: id}, nil, &existing); err != nil {
		return existing, false
	}
	return existing, existing.ID == id && existing.URL == origin
}
