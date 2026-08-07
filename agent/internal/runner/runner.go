// Package runner holds the pieces every subprocess-based agent adapter needs:
// runtime-settings plumbing, child process environment construction and
// session id generation. It exists so claudecode, cliagent and the cliagents
// protocol adapters do not each maintain their own copy.
package runner

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"slices"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// Settings embeds the shared runtime-settings selection and adds the
// model-switching convenience methods every subprocess session exposes, so
// sessions satisfy core.RuntimeSettingsSession and the model-switching
// interfaces by embedding one field.
type Settings struct {
	*core.RuntimeSettingsSelection
}

// NewSettings wraps a defaults+capabilities pair in a Settings value.
func NewSettings(defaults core.RuntimeSettings, capabilities core.RuntimeSettingsCapabilities) Settings {
	return Settings{core.NewRuntimeSettingsSelection(defaults, capabilities)}
}

func (s Settings) ModelSwitchingSupported() bool {
	return len(s.RuntimeSettingsCapabilities().Models) > 0
}

func (s Settings) CurrentModel() string { return s.CurrentRuntimeSettings().Model }

func (s Settings) DefaultModel() string { return s.DefaultRuntimeSettings().Model }

func (s Settings) SupportedModels() []string {
	options := s.RuntimeSettingsCapabilities().Models
	models := make([]string, 0, len(options))
	for _, option := range options {
		models = append(models, option.Value)
	}
	return models
}

func (s Settings) SetModel(model string) error {
	return s.SetRuntimeSetting(core.RuntimeSettingModel, model)
}

func (s Settings) ResetModel() error { return s.ResetRuntimeSetting(core.RuntimeSettingModel) }

// BuildEnv returns the parent environment plus extra entries, dropping any
// variable whose key is listed in dropKeys (e.g. CLAUDECODE so a nested
// claude can launch).
func BuildEnv(extra map[string]string, dropKeys ...string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+len(extra))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if slices.Contains(dropKeys, key) {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range extra {
		out = append(out, key+"="+value)
	}
	return out
}

// WithTraceparent replaces/sets TRACEPARENT so child telemetry joins the
// current observation trace. A blank traceparent leaves env untouched.
func WithTraceparent(env []string, traceparent string) []string {
	if traceparent == "" {
		return env
	}
	return OverrideEnv(env, map[string]string{"TRACEPARENT": traceparent})
}

// OverrideEnv returns env with the given keys replaced by the override values.
func OverrideEnv(env []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return env
	}
	filtered := make([]string, 0, len(env)+len(overrides))
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok {
			if _, replaced := overrides[key]; replaced {
				continue
			}
		}
		filtered = append(filtered, entry)
	}
	for key, value := range overrides {
		filtered = append(filtered, key+"="+value)
	}
	return filtered
}

// RandID returns a short random hex id for session naming.
func RandID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
