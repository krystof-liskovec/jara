package jara

// remoteWorkspaceState is what a sync reads from the host before it moves
// anything: the checkout's commit and branch, and its file list. A host sends
// it with a runner claim that asks for it (Request.SyncState), so opening a
// session needs no separate round trips for them. Hosts before it send none
// and the sync asks for each as before.
//
// Each part is used at most once and only while it still describes the host:
// anything that moves the host's branch or commits forgets it.
type remoteWorkspaceState struct {
	Git            *gitStateAnswer `json:"git,omitempty"`
	Files          Snapshot        `json:"files"`
	IncludeIgnored bool            `json:"include_ignored,omitempty"`
	// commit is the local commit Git.Contains answers for.
	commit string
}

// readRemoteWorkspaceState serves the sync state for a claim on the host.
func readRemoteWorkspaceState(q Request) (*remoteWorkspaceState, error) {
	w, err := workspaceInfo(q.ID)
	if err != nil {
		return nil, err
	}
	git := remoteGitState(w.Path, q.Commit)
	files, err := manifestWithIgnored(w.Path, q.IncludeIgnored)
	if err != nil {
		return nil, err
	}
	return &remoteWorkspaceState{Git: &git, Files: files, IncludeIgnored: q.IncludeIgnored}, nil
}

// remoteGitState is the host checkout's state; commit, when valid, is the
// client's commit for Contains.
func remoteGitState(path, commit string) gitStateAnswer {
	answer := gitStateAnswer{gitCheckoutState: checkoutState(path)}
	if answer.Head != "" && commitIDRE.MatchString(commit) {
		contains := hasCommit(path, commit) && isAncestor(path, commit, answer.Head)
		answer.Contains = &contains
	}
	return answer
}

// claimedState keeps the sync state a claim returned, answered for the local
// commit head. It is never nil, so later steps can remember what they fetch.
func claimedState(state *remoteWorkspaceState, head string) *remoteWorkspaceState {
	if state == nil {
		return &remoteWorkspaceState{}
	}
	state.commit = head
	return state
}

// gitState returns the host's checkout state with Contains answered for the
// local commit head, asking the host unless s already knows it. A nil s
// always asks.
func (s *remoteWorkspaceState) gitState(r Remote, b Binding, head string) (gitStateAnswer, error) {
	if s != nil && s.Git != nil && s.commit == head {
		return *s.Git, nil
	}
	var answer gitStateAnswer
	if err := rpc(r, Request{Op: "git-state", ID: b.Workspace, Commit: head}, nil, &answer); err != nil {
		return answer, err
	}
	if s != nil {
		s.Git, s.commit = &answer, head
	}
	return answer, nil
}

// manifest hands over the host's file list once, when it matches the
// requested listing.
func (s *remoteWorkspaceState) manifest(includeIgnored bool) (Snapshot, bool) {
	if s == nil || s.Files == nil || s.IncludeIgnored != includeIgnored {
		return nil, false
	}
	files := s.Files
	s.Files = nil
	return files, true
}

// forget drops everything known: the host's checkout changed or may have.
func (s *remoteWorkspaceState) forget() {
	if s != nil {
		*s = remoteWorkspaceState{}
	}
}
