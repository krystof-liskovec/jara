package jara

import "strings"

// /usage lists the usage limits of every Codex and Claude Code account Jara
// reaches, like jara usage: this device's, then each remote's.

func isChatUsageCommand(line string) bool {
	return line == "/usage" || strings.HasPrefix(line, "/usage ")
}

func (u *chatUI) runChatUsageCommand(line string) {
	only := strings.TrimSpace(strings.TrimPrefix(line, "/usage"))
	c, err := readConfig()
	if err != nil {
		u.printRole("system", "Could not read Jara's configuration: "+err.Error()+"\n")
		return
	}
	if _, ok := c.Remotes[only]; only != "" && !ok {
		u.printRole("system", "There is no remote named "+only+"; see jara remote list.\n")
		return
	}
	if u.runInBackground(func(func(string)) string { return "### Usage limits\n```\n" + collectUsage(c, only) + "```\n" }) {
		u.printRole("system", "Reading the usage limits…\n")
	}
}

// chatBackgroundNote is what background work prints: notes while it runs,
// then its result.
type chatBackgroundNote struct {
	text string
	done bool
}

// runInBackground runs work off the interface's goroutine, as reaching remotes
// takes a while; work may say things as it goes, and what it returns prints
// once done. One runs at a time.
func (u *chatUI) runInBackground(work func(say func(string)) string) bool {
	if u.backgroundRuns != nil {
		u.printRole("system", "The previous /mcp, /usage or /login command is still running; try again once it finished.\n")
		return false
	}
	notes := make(chan chatBackgroundNote, 16)
	u.backgroundRuns = notes
	go func() {
		say := func(text string) { notes <- chatBackgroundNote{text: text} }
		notes <- chatBackgroundNote{text: work(say), done: true}
	}()
	return true
}

func (u *chatUI) acceptChatBackground(note chatBackgroundNote) {
	if note.done {
		u.backgroundRuns, u.login.stopEverywhere = nil, nil
	}
	if note.text != "" {
		u.printRole("system", note.text)
	}
}
