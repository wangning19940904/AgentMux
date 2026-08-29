package core

// ModelSwitchingSession is an optional AgentSession capability for sessions
// whose runtime can choose a model per turn.
type ModelSwitchingSession interface {
	ModelSwitchingSupported() bool
	CurrentModel() string
	DefaultModel() string
	SupportedModels() []string
	SetModel(model string) error
	ResetModel() error
}

// ModelPickerState is the transport-neutral payload a platform can render as
// a model selection card.
type ModelPickerState struct {
	CurrentModel string
	DefaultModel string
	Options      []ModelPickerOption
}

type ModelPickerOption struct {
	Model   string
	Current bool
	Default bool
}
