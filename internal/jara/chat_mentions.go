package jara

import (
	"encoding/json"
	"sort"
	"strings"
)

// Typing @ followed by part of a path lists matching workspace files, found
// by the app-server's fuzzy file search on the workspace host. Choosing one
// inserts @path, which both providers read as a reference to that file.

const chatMentionLimit = 8

type chatMentionSearch struct {
	requested string   // newest query sent
	query     string   // query the files answer
	files     []string // workspace-relative paths, best match first
}

// chatMentionToken finds an @token that ends at the cursor.
func chatMentionToken(text string, cursor int) (start int, query string, ok bool) {
	if cursor < 0 || cursor > len(text) {
		return 0, "", false
	}
	if cursor < len(text) && !strings.ContainsRune(" \t\n", rune(text[cursor])) {
		return 0, "", false
	}
	before := text[:cursor]
	start = strings.LastIndexAny(before, " \t\n") + 1
	token := before[start:]
	if !strings.HasPrefix(token, "@") || strings.HasPrefix(token, "@@") {
		return 0, "", false
	}
	return start, token[1:], true
}

// noteChatComposer starts a file search when the input box ends in a new
// @query.
func (u *chatUI) noteChatComposer(text string, cursor int) {
	_, query, ok := chatMentionToken(text, cursor)
	if !ok || query == "" || query == u.mentions.requested || !u.connected || u.cwd == "" {
		return
	}
	u.mentions.requested = query
	_ = u.requestWithAction("fuzzyFileSearch", map[string]any{"query": query, "roots": []string{u.cwd}}, query)
}

func (u *chatUI) acceptChatMentionResponse(m appMessage, p chatPending) {
	var result struct {
		Files []struct {
			Path  string `json:"path"`
			Type  string `json:"match_type"`
			Score int    `json:"score"`
		} `json:"files"`
	}
	if m.Error != nil || json.Unmarshal(m.Result, &result) != nil || p.action != u.mentions.requested {
		return
	}
	sort.SliceStable(result.Files, func(i, j int) bool { return result.Files[i].Score > result.Files[j].Score })
	u.mentions.query, u.mentions.files = p.action, nil
	for _, file := range result.Files {
		path := chatText(file.Path)
		if file.Type == "directory" {
			path += "/"
		}
		if path != "" && !strings.ContainsAny(path, "\r\n") {
			u.mentions.files = append(u.mentions.files, path)
		}
		if len(u.mentions.files) == chatMentionLimit*4 {
			break
		}
	}
}

func chatMentionCandidates(text string, start, cursor int, query string, context chatCompletionContext) []chatEditorCandidate {
	if query == "" {
		return nil
	}
	var candidates []chatEditorCandidate
	for _, file := range context.Files {
		if !chatSubsequence(strings.ToLower(file.Value), strings.ToLower(query)) {
			continue
		}
		candidates = append(candidates, chatEditorCandidate{
			choice:      chatCompletionChoice{Value: "@" + file.Value, Description: file.Description},
			start:       start,
			end:         cursor,
			replacement: "@" + file.Value,
			append:      " ",
		})
		if len(candidates) == chatMentionLimit {
			break
		}
	}
	if len(candidates) == 1 && candidates[0].replacement == text[start:cursor] {
		return nil // already complete
	}
	return candidates
}

func chatSubsequence(text, pattern string) bool {
	for _, r := range pattern {
		index := strings.IndexRune(text, r)
		if index < 0 {
			return false
		}
		text = text[index+len(string(r)):]
	}
	return true
}

func chatMentionOptions(search chatMentionSearch) []chatCompletionOption {
	options := make([]chatCompletionOption, 0, len(search.files))
	for _, path := range search.files {
		description := "file"
		if strings.HasSuffix(path, "/") {
			description = "folder"
		}
		options = append(options, chatCompletionOption{Value: path, Description: description})
	}
	return options
}
