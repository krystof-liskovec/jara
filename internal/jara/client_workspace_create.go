package jara

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func createClientWorkspace(r Remote, chosen, root, origin string, copyMode, includeIgnored, separate bool) (Binding, error) {
	var err error
	id := workspaceID(root, origin)
	if separate {
		id += "-" + strings.ToLower(rand.Text()[:8])
	}
	q := Request{Op: "create", ID: id, Name: filepath.Base(root)}
	q.SourceHost, _ = os.Hostname()
	q.SourcePath = root
	if !copyMode {
		q.URL = origin
		q.Commit, err = gitOutput(root, "rev-parse", "HEAD")
		if err != nil {
			return Binding{}, err
		}
		q.Branch, _ = gitOutput(root, "symbolic-ref", "--short", "-q", "HEAD")
	}
	reusedExisting := false
	if q.URL != "" && !separate {
		legacyID := workspaceID(root, "")
		if legacyID != id {
			if existing, matches := workspaceForOrigin(r, legacyID, q.URL); matches {
				id, q.ID = legacyID, legacyID
				if existing.Ready {
					reusedExisting = true
					fmt.Fprintf(os.Stderr, "Reusing existing remote workspace %s; its checkout was left unchanged.\n", id)
				}
			}
		}
	}
	if !reusedExisting {
		if err = rpc(r, q, nil, nil); err != nil {
			destinationExists := strings.Contains(err.Error(), "destination already exists; no files changed:")
			if q.URL != "" && destinationExists {
				var existing Workspace
				if infoErr := rpc(r, Request{Op: "info", ID: id}, nil, &existing); infoErr == nil && existing.Ready && existing.ID == id && existing.URL == q.URL {
					reusedExisting = true
					fmt.Fprintf(os.Stderr, "Reusing existing remote workspace %s; its checkout was left unchanged.\n", id)
				}
			}
			if !reusedExisting {
				if destinationExists {
					return Binding{}, fmt.Errorf("%w\nThe existing workspace could not be verified as a ready clone of this repository; no files were changed.", err)
				}
				if q.URL != "" {
					return Binding{}, fmt.Errorf("%w\nIf this is a private repository, run `jara auth git --remote %s` from the checkout and retry.", err, chosen)
				}
				return Binding{}, err
			}
		}
	}
	b := Binding{Remote: chosen, Workspace: id}
	if reusedExisting {
		if err = seedExistingBaseline(b, root); err != nil {
			return Binding{}, err
		}
	}
	if !copyMode && !reusedExisting {
		bundle, cleanup, e := repositoryBundle(root)
		if e != nil {
			return Binding{}, e
		}
		defer cleanup()
		if e = rpc(r, Request{Op: "checkout", ID: id, Branch: q.Branch, Commit: q.Commit}, bundle, nil); e != nil {
			return Binding{}, e
		}
		if e = seedCloneBaseline(r, b, root); e != nil {
			fmt.Fprintln(os.Stderr, "Clone succeeded but initial sync baseline could not be recorded:", e)
		}
	}
	if copyMode {
		if err = syncWorkspace(r, b, root, "push", false, includeIgnored, false); err != nil {
			return Binding{}, err
		}
	}
	return b, nil
}
