package jara

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"gitea.liskovec.dev/krystof.liskovec/jara/internal/agentrouter"
	"gitea.liskovec.dev/krystof.liskovec/jara/internal/portable"
)

// Catching up moves conversations from the host to a device, usually one
// that left them there and has most of each session file already. For each
// conversation that changed since it last saw them, the device tells the
// host what it has of each session file: its size and the hash of its end,
// in its own checkout's form and converted to the host's (runnerHave). When
// the host's copy starts with those bytes, it sends only the rest, from a line
// boundary, with the SHA-256 of its whole copy; the device checks it and
// appends the rest converted to its checkout. Anything else, or a check that
// fails, sends whole files.

// runnerHave is what a device has of one session file, in one checkout form.
type runnerHave struct {
	ThreadID  string `json:"thread_id"`
	Agent     string `json:"agent"`
	BackendID string `json:"backend_id"`
	Root      string `json:"root"`
	Size      int64  `json:"size"`
	Tail      string `json:"tail"`
}

type runnerExportRequest struct {
	Haves []runnerHave `json:"haves,omitempty"`
}

var errStaleDelta = errors.New("a conversation file changed here since it was described")

// collectHaves describes this device's session files of the conversations
// threads, checked out at root, for a host whose checkout is at hostRoot.
func collectHaves(root, hostRoot string, threads []string) []runnerHave {
	rollouts := portable.IndexCodexRollouts()
	var haves []runnerHave
	for _, id := range threads {
		thread := agentrouter.LoadThread(id)
		if thread == nil {
			continue
		}
		for _, file := range threadNativeFiles(thread, rollouts) {
			info, err := os.Stat(file.local)
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > portable.MaxNativeFile {
				continue
			}
			data, err := os.ReadFile(file.local)
			if err != nil {
				continue
			}
			forms := map[string][]byte{root: data}
			if hostRoot != "" && hostRoot != root {
				forms[hostRoot] = portable.RewritePath(data, root, hostRoot)
			}
			for formRoot, form := range forms {
				haves = append(haves, runnerHave{ThreadID: id, Agent: file.Agent, BackendID: file.BackendID,
					Root: formRoot, Size: int64(len(form)), Tail: tailHash(form)})
			}
		}
	}
	return haves
}

// storedDelta makes file the rest of the host's copy at path after what the
// device has, when the copy starts with those bytes and the rest starts a
// line.
func storedDelta(path string, haves []runnerHave, thread, root string, file *runnerNativeFile) bool {
	var have *runnerHave
	for i := range haves {
		h := &haves[i]
		if h.ThreadID == thread && h.Agent == file.Agent && h.BackendID == file.BackendID && h.Root == root {
			have = h
		}
	}
	if have == nil || have.Size <= 0 {
		return false
	}
	handle, err := os.Open(path)
	if err != nil {
		return false
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || info.Size() < have.Size {
		return false
	}
	boundary := make([]byte, 1)
	if _, err = handle.ReadAt(boundary, have.Size-1); err != nil || boundary[0] != '\n' {
		return false
	}
	if tail, tailErr := fileTail(path, have.Size); tailErr != nil || tail != have.Tail {
		return false
	}
	rest := make([]byte, info.Size()-have.Size)
	if _, err = handle.ReadAt(rest, have.Size); err != nil && len(rest) > 0 {
		return false
	}
	h := sha256.New()
	if hashFilePrefix(h, path, info.Size()) != nil {
		return false
	}
	file.Base, file.Tail, file.Sum = have.Size, have.Tail, hex.EncodeToString(h.Sum(nil))
	file.setData(rest)
	return true
}

// appendImportedDelta applies a delta from a checkpoint stored from
// bundleRoot to this device's file at dest, checked out at root.
func appendImportedDelta(dest, bundleRoot, root string, file runnerNativeFile) error {
	current, err := os.ReadFile(dest)
	if err != nil {
		return errStaleDelta
	}
	form := current
	if bundleRoot != root {
		form = portable.RewritePath(current, root, bundleRoot)
	}
	h := sha256.New()
	h.Write(form)
	h.Write(file.bytes())
	if int64(len(form)) != file.Base || hex.EncodeToString(h.Sum(nil)) != file.Sum {
		return errStaleDelta
	}
	if file.size() == 0 {
		return nil
	}
	handle, err := os.OpenFile(dest, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	if _, err = handle.Write(portable.RewritePath(file.bytes(), bundleRoot, root)); err != nil {
		handle.Close()
		return err
	}
	return handle.Close()
}

// fetchCheckpoint exports the conversations changed after since from the
// host, as deltas against what this device has, and installs them. A delta
// that does not apply makes it fetch whole files once more.
func fetchCheckpoint(o localRunnerOpen, since int64, hostRoot string, changed []string) (runnerCheckpoint, []string, error) {
	q := Request{Op: "runner-export", ID: o.binding.Workspace, Since: since, Compress: true}
	request := runnerExportRequest{Haves: collectHaves(o.root, hostRoot, changed)}
	for {
		var checkpoint runnerCheckpoint
		if err := rpcCompressed(o.remote, q, request, &checkpoint); err != nil {
			return checkpoint, nil, err
		}
		briefed, err := importCheckpoint(checkpoint, o.root)
		if errors.Is(err, errStaleDelta) && len(request.Haves) > 0 {
			request.Haves = nil
			continue
		}
		if err != nil {
			return checkpoint, nil, err
		}
		received, whole := 0, 0
		for _, bundle := range checkpoint.Threads {
			for _, file := range bundle.Files {
				received += file.size()
				if file.Base == 0 {
					whole++
				}
			}
		}
		if os.Getenv("JARA_RUNNER_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "Caught up: %d conversation(s), %d byte(s) of session files, %d sent whole.\n", len(checkpoint.Threads), received, whole)
		}
		return checkpoint, briefed, nil
	}
}

// sentAfterImport records that the host's copies of the imported
// conversations' session files match this device's, so the next checkpoint
// sends deltas.
func sentAfterImport(sent map[string]runnerSentFile, checkpoint runnerCheckpoint) map[string]runnerSentFile {
	if sent == nil {
		sent = map[string]runnerSentFile{}
	}
	rollouts := portable.IndexCodexRollouts()
	for _, bundle := range checkpoint.Threads {
		thread := agentrouter.LoadThread(bundle.ThreadID)
		if thread == nil {
			continue
		}
		for _, file := range threadNativeFiles(thread, rollouts) {
			data, err := os.ReadFile(file.local)
			if err != nil {
				delete(sent, file.local)
				continue
			}
			sent[file.local] = runnerSentFile{Size: int64(len(data)), Tail: tailHash(data)}
		}
	}
	return sent
}
