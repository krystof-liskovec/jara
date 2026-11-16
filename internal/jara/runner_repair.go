package jara

import (
	"fmt"
	"maps"
	"strings"
)

// sendCheckpoint sends a checkpoint whose session files go as deltas against
// what the receiver stores (prev), repairing files the receiver's copy does
// not match, at most twice. collect builds the checkpoint for a sent state;
// send delivers it. It returns the receiver's answer and what it now stores.
func sendCheckpoint(prev map[string]runnerSentFile, verify bool, receiver string,
	collect func(*runnerSentState) (runnerCheckpoint, error),
	send func(runnerCheckpoint) (runnerCheckpointAnswer, error)) (runnerCheckpointAnswer, map[string]runnerSentFile, error) {
	sent := &runnerSentState{Prev: maps.Clone(prev), Verify: verify}
	if sent.Prev == nil {
		sent.Prev = map[string]runnerSentFile{}
	}
	for attempt := 1; ; attempt++ {
		sent.Next = map[string]runnerSentFile{}
		checkpoint, err := collect(sent)
		if err != nil {
			return runnerCheckpointAnswer{}, nil, err
		}
		answer, err := send(checkpoint)
		switch {
		case err == nil && len(answer.Resync) == 0:
			stored := sent.Prev
			maps.Copy(stored, sent.Next)
			return answer, stored, nil
		case attempt >= 3:
			if err == nil {
				err = fmt.Errorf("conversation files still differ from %s's copy after repair", receiver)
			}
			return answer, nil, err
		case err != nil && strings.Contains(err.Error(), runnerResendMessage):
			clear(sent.Prev) // a receiver that cannot repair: send whole files
		case err != nil:
			return answer, nil, err
		default:
			repairSent(sent.Prev, checkpoint, answer.Resync)
		}
	}
}

// repairSent points each session file the receiver's copy did not match at
// what the receiver has: a delta continues there when the sender's file has
// the same bytes, and the whole file goes otherwise.
func repairSent(sent map[string]runnerSentFile, checkpoint runnerCheckpoint, resync []runnerResync) {
	for _, have := range resync {
		for _, bundle := range checkpoint.Threads {
			for _, file := range bundle.Files {
				if bundle.ThreadID != have.ThreadID || file.Agent != have.Agent || file.BackendID != have.BackendID || file.local == "" {
					continue
				}
				if state, ok := resumeFrom(file.local, have.Size, have.Tail); ok {
					sent[file.local] = state
				} else {
					delete(sent, file.local)
				}
			}
		}
	}
}
