package protocol

const (
	TurnSelectionGet     = "jara/turnSelection/get"
	TurnSelectionSet     = "jara/turnSelection/set"
	TurnSelectionUpdated = "jara/turnSelection/updated"
)

type TurnSelection struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type TurnSelectionUpdate struct {
	ThreadID string         `json:"threadId"`
	Next     TurnSelection  `json:"next"`
	Applied  *TurnSelection `json:"applied,omitempty"`
}
