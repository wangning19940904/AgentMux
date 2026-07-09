package core

import (
	"fmt"
	"strings"
	"sync"
)

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

// ModelSelection stores the default and per-session model override for
// turn-based agent sessions.
type ModelSelection struct {
	mu              sync.Mutex
	defaultModel    string
	currentOverride string
	supportedModels []string
}

func NewModelSelection(defaultModel string, supportedModels []string) *ModelSelection {
	seen := map[string]bool{}
	var models []string
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		models = append(models, model)
	}
	add(defaultModel)
	for _, model := range supportedModels {
		add(model)
	}
	return &ModelSelection{
		defaultModel:    strings.TrimSpace(defaultModel),
		supportedModels: models,
	}
}

func ModelSelectionFromConfig(cfg map[string]any) *ModelSelection {
	defaultModel, _ := cfg["model"].(string)
	var supported []string
	if raw, ok := cfg["supported_models"].([]string); ok {
		supported = append(supported, raw...)
	} else if raw, ok := cfg["supported_models"].([]any); ok {
		for _, item := range raw {
			if model, ok := item.(string); ok {
				supported = append(supported, model)
			}
		}
	}
	return NewModelSelection(defaultModel, supported)
}

func (m *ModelSelection) CurrentModel() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.currentOverride != "" {
		return m.currentOverride
	}
	return m.defaultModel
}

func (m *ModelSelection) DefaultModel() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defaultModel
}

func (m *ModelSelection) SupportedModels() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.supportedModels...)
}

func (m *ModelSelection) SetModel(model string) error {
	if m == nil {
		return fmt.Errorf("model switching is not supported by this runtime")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return fmt.Errorf("model is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, supported := range m.supportedModels {
		if supported == model {
			m.currentOverride = model
			return nil
		}
	}
	if len(m.supportedModels) == 0 {
		return fmt.Errorf("this runtime has no selectable models")
	}
	return fmt.Errorf("model %q is not supported", model)
}

func (m *ModelSelection) ResetModel() error {
	if m == nil {
		return fmt.Errorf("model switching is not supported by this runtime")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentOverride = ""
	return nil
}
