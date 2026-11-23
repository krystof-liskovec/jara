package jara

import (
	"encoding/json"
	"sort"
	"strings"
)

// Typing @ lists the workspace's files. `@` and `@folder/` browse like a
// shell, listing the folder (the router answers where the workspace is);
// other text also gets the app-server's fuzzy file search. Choosing a file
// inserts @path, which both providers read as a reference to that file;
// choosing a folder inserts @folder/ and lists it.

const (
	chatMentionLimit       = 8
	chatMentionFolderLimit = 200
)

type chatMentionSearch struct {
	requested string   // newest query sent
	query     string   // query the files answer
	files     []string // workspace-relative paths, best match first

	folderRequested string                 // newest folder listing asked for: cwd NUL folder
	folderKey       string                 // the listing entries answer
	entries         []chatCompletionOption // the folder's entries; folders end in /
}

// chatMentionFolder splits a query into the folder it browses and the
// prefix of the entry; browse is false without a / (only top level).
func chatMentionFolder(query string) (folder, prefix string, browse bool) {
	i := strings.LastIndex(query, "/")
	if i < 0 {
		return "", query, false
	}
	return query[:i+1], query[i+1:], true
}

func chatMentionFolderKey(cwd, folder string) string { return cwd + "\x00" + folder }

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

// noteChatComposer lists the folder an @query browses, and starts a file
// search for a new @query without a folder.
func (u *chatUI) noteChatComposer(text string, cursor int) {
	_, query, ok := chatMentionToken(text, cursor)
	if !ok || !u.connected || u.cwd == "" {
		return
	}
	folder, _, browse := chatMentionFolder(query)
	if key := chatMentionFolderKey(u.cwd, folder); key != u.mentions.folderRequested {
		u.mentions.folderRequested = key
		_ = u.requestWithAction("jara/files/list", map[string]string{"cwd": u.cwd, "path": folder}, key)
	}
	if browse || query == "" || query == u.mentions.requested {
		return
	}
	u.mentions.requested = query
	_ = u.requestWithAction("fuzzyFileSearch", map[string]any{"query": query, "roots": []string{u.cwd}}, query)
}

func (u *chatUI) acceptChatFolderResponse(m appMessage, p chatPending) {
	if p.action != u.mentions.folderRequested {
		return
	}
	var result struct {
		Entries []struct {
			Name string `json:"name"`
			Dir  bool   `json:"dir"`
		} `json:"entries"`
	}
	u.mentions.folderKey, u.mentions.entries = p.action, nil
	if m.Error != nil || json.Unmarshal(m.Result, &result) != nil {
		return // an older host, or no such folder: nothing to list
	}
	for _, entry := range result.Entries {
		// A mention ends at whitespace, so such names cannot be completed.
		if entry.Name == "" || chatText(entry.Name) != entry.Name || strings.ContainsAny(entry.Name, " \t\r\n/") {
			continue
		}
		option := chatCompletionOption{Value: entry.Name, Description: "file"}
		if entry.Dir {
			option = chatCompletionOption{Value: entry.Name + "/", Description: "folder"}
		}
		u.mentions.entries = append(u.mentions.entries, option)
	}
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
	folder, prefix, browse := chatMentionFolder(query)
	var candidates []chatEditorCandidate
	seen := map[string]bool{}
	add := func(path, description string) {
		if seen[path] {
			return
		}
		seen[path] = true
		candidate := chatEditorCandidate{
			choice:      chatCompletionChoice{Value: "@" + path, Description: description},
			start:       start,
			end:         cursor,
			replacement: "@" + path,
			append:      " ",
		}
		if strings.HasSuffix(path, "/") {
			candidate.append, candidate.keepOpen = "", true // go on into the folder
		}
		candidates = append(candidates, candidate)
	}
	if context.FolderListed && context.Folder == folder {
		hidden := strings.HasPrefix(prefix, ".")
		for _, entry := range context.FolderEntries {
			if strings.HasPrefix(entry.Value, ".") && !hidden || !strings.HasPrefix(strings.ToLower(entry.Value), strings.ToLower(prefix)) {
				continue
			}
			add(folder+entry.Value, entry.Description)
			if len(candidates) == chatMentionFolderLimit {
				break
			}
		}
	}
	if !browse && query != "" {
		limit := len(candidates) + chatMentionLimit
		for _, file := range context.Files {
			if chatSubsequence(strings.ToLower(file.Value), strings.ToLower(query)) {
				add(file.Value, file.Description)
			}
			if len(candidates) == limit {
				break
			}
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
