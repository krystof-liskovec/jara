package jara

import (
	"encoding/json"
	"io"
	"time"

	"krystof.liskovec/jara/internal/protocol"
)

// chatUI is one interface session. Its state is grouped by lifetime so each
// group can be reset as a whole: the turn state ends with each turn and the
// conversation state ends when another conversation opens.
type chatUI struct {
	c                 *appConnection
	out               io.Writer
	cwd, thread       string
	threadReady       bool
	connected         bool
	pending           map[string]chatPending
	pendingTurnModels map[string]chatTurnRequest
	items             map[string]json.RawMessage // items of the running turn, by ID

	chatSessionOptions
	chatTurnState
	chatConversationState
	chatModelState
	chatComposerState
	chatBrowserState
	chatExtras
	chatReconnectState
}

// chatSessionOptions describe how this interface was started.
type chatSessionOptions struct {
	startedAt         time.Time
	workspaceLabel    string
	trackAgentState   bool
	persistentSession bool
	resumeAtStart     bool
	resumeLatest      bool
	lastAgentState    string
	manualApprovals   bool // auto mode is the default
}

// chatTurnState lives while a turn runs.
type chatTurnState struct {
	turn                  string
	busy, cancel          bool
	turnStartedAt         time.Time
	attachedTurnActive    bool
	activityLabel         string
	activitySummaryItemID string
	activitySummaryIndex  int
	activitySummaryText   string
	approvals             []appMessage
	questionRequests      []chatQuestionRequest
}

// chatConversationState belongs to the open conversation and is reset when
// another one opens.
type chatConversationState struct {
	planMode                 bool
	goal                     *protocol.ThreadGoal
	nextModel                string
	nextModelLabel           string
	nextReasoning            string
	lastPrompt               string // most recent prompt, for a fallback retry
	fallbackRetried          bool   // lastPrompt was already retried on the fallback
	promptLog                []chatPromptEntry
	contextUsage             chatContextUsage
	reasoningSummaryStreamed map[string]bool
	reasoningSummaryIndexes  map[string]int
	subagentPaths            map[string]string
	subagentItemDisplays     map[string]string
	chatAgentsState          // subagents, and which one is shown
}

// chatModelState tracks the models in use and the catalog.
type chatModelState struct {
	defaults                     *protocol.DefaultModels // host defaults; nil while loading
	models                       *chatModelBrowser
	effectiveModel               string
	effectiveReasoning           string
	resolvedModel                string // concrete model behind a Claude alias
	modelSelectionGeneration     uint64
	reasoningSelectionGeneration uint64
}

// chatComposerState covers drafts and queued prompts.
type chatComposerState struct {
	composing           bool
	draft               []string
	draftBytes          int
	queuedPrompts       []chatQueuedPrompt
	queuePromptSequence uint64
	queueEditRequest    uint64
	prefill             string // text to put back in the input box
}

// chatBrowserState covers the pickers and the changes pane.
type chatBrowserState struct {
	pickerRequested        string
	sessions               *chatSessionBrowser
	sessionPickerRequested bool
	diff                   chatDiffState
}

// chatExtras holds the conveniences around the conversation.
type chatExtras struct {
	afterHistory   string                   // note shown once the transcript reloads
	alert          func(string)             // writes an alert to the terminal; nil when not interactive
	alertsOff      bool                     // /alerts off
	mentions       chatMentionSearch        // @file completion
	images         map[int]string           // [Image #N] placeholders → data URLs
	customCommands []protocol.CustomCommand // prompt files usable as slash commands
}
