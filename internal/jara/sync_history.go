package jara

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// A history rewrite (a rebase, an amended commit, a force-pushed branch) on
// either machine leaves one checkout on commits the other no longer builds
// on, so commits cannot fast-forward in either direction. On a terminal the
// user picks the history to keep and the other checkout moves onto it. Nothing
// is lost: the moved checkout's previous commit stays on a backup branch and
// its uncommitted changes in a Git stash.

// divergedHistoryError reports checkouts where neither commit builds on the
// other. The commit lists are known only when this checkout has the remote
// commit.
type divergedHistoryError struct {
	Local, Remote                 gitCheckoutState
	LocalOnly, RemoteOnly         int      // commits on one side the other does not build on
	LocalSubjects, RemoteSubjects []string // the newest of them, "short-hash subject"
}

func (e *divergedHistoryError) Error() string {
	return fmt.Sprintf("local and remote histories have diverged (this checkout is at %s, the remote checkout at %s, as after a rebase or force-push); run `jara sync push` or `jara sync pull` in a terminal to choose which history to keep, or merge or rebase with Git, then retry (nothing transferred)", shortCommit(e.Local.Head), shortCommit(e.Remote.Head))
}

func newDivergedHistoryError(root string, local, remote gitCheckoutState) *divergedHistoryError {
	e := &divergedHistoryError{Local: local, Remote: remote}
	if hasCommit(root, remote.Head) {
		e.LocalOnly, e.LocalSubjects = commitsBetween(root, remote.Head, local.Head)
		e.RemoteOnly, e.RemoteSubjects = commitsBetween(root, local.Head, remote.Head)
	}
	return e
}

// commitsBetween counts the commits in from..to and lists the newest five.
func commitsBetween(root, from, to string) (int, []string) {
	count := 0
	if text, err := gitOutput(root, "rev-list", "--count", from+".."+to); err == nil {
		fmt.Sscan(text, &count)
	}
	var subjects []string
	if log, err := gitOutput(root, "log", "--max-count=5", "--format=%h %s", from+".."+to); err == nil && log != "" {
		subjects = strings.Split(log, "\n")
	}
	return count, subjects
}

// historyKept is the history the user chose after a divergence.
type historyKept int

const (
	historyKeptNeither historyKept = iota
	historyKeptLocal               // the remote checkout moved onto this one
	historyKeptRemote              // this checkout moved onto the remote one
)

// syncWorkspaceOfferingHistoryReplacement runs syncWorkspace and, when it
// stops on diverged histories, lets the user choose the history to keep, then
// syncs again: in the requested direction after the remote moved, and as a
// pull (bringing the remote's uncommitted edits) after this checkout moved.
// reader may be nil.
func syncWorkspaceOfferingHistoryReplacement(r Remote, b Binding, root, direction string, force bool, reader *bufio.Reader, options ...bool) error {
	err := syncWorkspace(r, b, root, direction, force, options...)
	kept, offerErr := offerHistoryReplacement(r, b, root, reader, err)
	switch {
	case offerErr != nil:
		return offerErr
	case kept == historyKeptLocal:
		return syncWorkspace(r, b, root, direction, force, options...)
	case kept == historyKeptRemote:
		includeIgnored := len(options) > 0 && options[0]
		return syncWorkspace(r, b, root, "pull", false, includeIgnored)
	}
	return err
}

// offerHistoryReplacement asks, on a terminal, which history to keep when err
// reports diverged histories, and moves the other checkout onto it. It does
// nothing for other errors or without a terminal.
func offerHistoryReplacement(r Remote, b Binding, root string, reader *bufio.Reader, err error) (historyKept, error) {
	var diverged *divergedHistoryError
	if !errors.As(err, &diverged) || !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stderr.Fd())) {
		return historyKeptNeither, nil
	}
	if reader == nil {
		reader = bufio.NewReader(os.Stdin)
	}
	fmt.Fprint(os.Stderr, historyReplacementPrompt(diverged)+" ")
	answer, readErr := reader.ReadString('\n')
	if readErr != nil && (readErr != io.EOF || answer == "") {
		if readErr == io.EOF {
			return historyKeptNeither, nil
		}
		return historyKeptNeither, readErr
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "l", "local":
		return historyKeptLocal, replaceRemoteHistory(r, b, root, diverged.Local, diverged.Remote)
	case "r", "remote":
		return historyKeptRemote, moveLocalHistory(r, b, root, diverged.Local, diverged.Remote)
	}
	return historyKeptNeither, nil
}

func historyReplacementPrompt(e *divergedHistoryError) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "This checkout (%s) and the remote checkout (%s) are on diverged histories, as after a rebase or force-push.", shortCommit(e.Local.Head), shortCommit(e.Remote.Head))
	writeCommitList(&prompt, "This checkout", e.LocalOnly, e.LocalSubjects)
	if e.RemoteOnly == 0 && len(e.RemoteSubjects) == 0 {
		prompt.WriteString("\nThe remote checkout has commits this checkout does not have.")
	} else {
		writeCommitList(&prompt, "The remote checkout", e.RemoteOnly, e.RemoteSubjects)
	}
	prompt.WriteString("\nKeep which history? The other checkout moves onto it; its current commit stays on a backup branch and its uncommitted changes go to a Git stash.")
	prompt.WriteString("\n  l  this checkout's: move the remote checkout")
	prompt.WriteString("\n  r  the remote's: move this checkout")
	prompt.WriteString("\n  n  neither: reconcile them with Git (default)")
	prompt.WriteString("\nChoice [l/r/N]")
	return prompt.String()
}

func writeCommitList(prompt *strings.Builder, side string, count int, subjects []string) {
	if count == 0 {
		return
	}
	fmt.Fprintf(prompt, "\n%s has %d commit(s) the other does not build on", side, count)
	if len(subjects) > 0 {
		prompt.WriteString(", newest first:")
		for _, subject := range subjects {
			prompt.WriteString("\n    " + subject)
		}
	}
}
