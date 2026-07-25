package provider

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// codexModelProviderID is the single [model_providers.*] block AgentMux
// owns in ~/.codex/config.toml (cc-switch uses "custom" the same way, see
// CC_SWITCH_CODEX_MODEL_PROVIDER_ID). A fixed id means switches never
// accumulate stale per-provider blocks.
const codexModelProviderID = "agentmux"

// codexModelCatalogFilename mirrors cc-switch's cc-switch-model-catalog.json:
// a catalog file the Codex desktop app reads so third-party models appear in
// its model picker.
const codexModelCatalogFilename = "agentmux-model-catalog.json"

// codexReservedProviderIDs are Codex built-in provider ids we must never
// delete or overwrite (kept in sync with cc-switch CODEX_RESERVED_MODEL_PROVIDER_IDS).
var codexReservedProviderIDs = map[string]bool{
	"amazon-bedrock": true,
	"openai":         true,
	"ollama":         true,
	"lmstudio":       true,
	"oss":            true,
	"ollama-chat":    true,
}

// writeCodexConfig points ~/.codex/config.toml at provider p. Mirroring
// cc-switch's preserve-official-auth path (codex_config.rs
// write_codex_live_for_provider): auth.json is NEVER touched, so the user's
// ChatGPT OAuth login survives; the third-party key rides in the provider
// block's experimental_bearer_token instead. Official providers restore the
// built-in defaults.
func writeCodexConfig(home string, p, prev *core.Provider) error {
	dir := codexConfigDir(home, p)
	path := filepath.Join(dir, "config.toml")
	doc := readTOMLObject(path)

	cleanupLegacyCodexBlocks(doc, prev)

	if isCodexOfficial(p) {
		delete(doc, "model_provider")
		if providers, ok := doc["model_providers"].(map[string]any); ok {
			delete(providers, codexModelProviderID)
			if len(providers) == 0 {
				delete(doc, "model_providers")
			}
		}
		if p.Model != "" {
			doc["model"] = p.Model
		}
		if err := syncCodexModelCatalog(dir, doc, nil); err != nil {
			return err
		}
		return writeTOMLObject(path, doc)
	}

	doc["model_provider"] = codexModelProviderID
	if p.Model != "" {
		doc["model"] = p.Model
	}
	providers := ensureMap(doc, "model_providers")
	block := map[string]any{
		"name":     p.Name,
		"base_url": p.BaseURL,
		"wire_api": codexWireAPI(p),
	}
	if key := providerAPIKey(p); key != "" {
		block["experimental_bearer_token"] = key
	}
	providers[codexModelProviderID] = block

	if err := syncCodexModelCatalog(dir, doc, codexCatalogModels(p)); err != nil {
		return err
	}
	return writeTOMLObject(path, doc)
}

// cleanupLegacyCodexBlocks removes the per-provider block older AgentMux
// builds keyed by provider id, so switches stop accumulating stale tables.
func cleanupLegacyCodexBlocks(doc map[string]any, prev *core.Provider) {
	if prev == nil {
		return
	}
	providers, ok := doc["model_providers"].(map[string]any)
	if !ok {
		return
	}
	legacy := legacyCodexProviderID(prev)
	if legacy == "" || legacy == codexModelProviderID || codexReservedProviderIDs[legacy] {
		return
	}
	delete(providers, legacy)
	if doc["model_provider"] == legacy {
		delete(doc, "model_provider")
	}
	if len(providers) == 0 {
		delete(doc, "model_providers")
	}
}

// isCodexOfficial reports whether p should restore Codex's built-in provider
// (ChatGPT OAuth / auth.json), mirroring cc-switch category == "official".
func isCodexOfficial(p *core.Provider) bool {
	if p == nil {
		return false
	}
	if p.Category != "official" {
		return false
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	return base == "" || base == "https://api.openai.com/v1" || base == "https://api.openai.com"
}

// legacyCodexProviderID reproduces the sanitized id older builds used as the
// [model_providers.<id>] key.
func legacyCodexProviderID(p *core.Provider) string {
	if v := configString(p, "codex_provider_id"); v != "" {
		return v
	}
	id := p.ID
	if id == "" {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return '_'
	}, id)
}

func codexWireAPI(p *core.Provider) string {
	if p.Meta.CodexWireAPI != "" {
		return p.Meta.CodexWireAPI
	}
	switch p.Meta.APIFormat {
	case "openai_responses":
		return "responses"
	case "openai_chat":
		return "chat"
	default:
		if strings.Contains(strings.ToLower(p.ID), "openai") {
			return "responses"
		}
		return "chat"
	}
}

// codexCatalogModels resolves the desktop-visible model list: the provider's
// default model first, then any extra supported models, deduped.
func codexCatalogModels(p *core.Provider) []string {
	seen := map[string]bool{}
	var out []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	add(p.Model)
	for _, m := range p.Meta.SupportedModels {
		add(m)
	}
	return out
}

// syncCodexModelCatalog writes (or unlinks) the AgentMux model catalog and
// keeps config.toml's model_catalog_json pointer in sync. The pointer is only
// removed when it references our own file (cc-switch ownership rule).
func syncCodexModelCatalog(dir string, doc map[string]any, models []string) error {
	if len(models) == 0 {
		if ptr, ok := doc["model_catalog_json"].(string); ok && filepath.Base(ptr) == codexModelCatalogFilename {
			delete(doc, "model_catalog_json")
			_ = os.Remove(filepath.Join(dir, codexModelCatalogFilename))
		}
		return nil
	}
	entries := make([]any, 0, len(models))
	for i, model := range models {
		entries = append(entries, codexCatalogEntry(model, i))
	}
	catalog := map[string]any{"models": entries}
	if err := writeJSONObject(filepath.Join(dir, codexModelCatalogFilename), catalog); err != nil {
		return err
	}
	doc["model_catalog_json"] = codexModelCatalogFilename
	return nil
}

// codexCatalogEntry mirrors cc-switch's native-responses catalog template
// (codex_native_responses_template.json + codex_catalog_model_entry): a clean
// entry with shell_command edits and no freeform apply_patch tool, which is
// what third-party gateways accept.
func codexCatalogEntry(model string, priority int) map[string]any {
	const contextWindow = 128000
	return map[string]any{
		"slug":                    model,
		"display_name":            model,
		"description":             model,
		"base_instructions":       "You are Codex, a coding agent. You and the user share the same workspace and collaborate to achieve the user's goals.",
		"default_reasoning_level": "high",
		"supported_reasoning_levels": []any{
			map[string]any{"effort": "none", "description": "Disable Thinking"},
			map[string]any{"effort": "high", "description": "Enabled Thinking"},
		},
		"shell_type":                       "shell_command",
		"visibility":                       "list",
		"supported_in_api":                 true,
		"priority":                         1000 + priority,
		"supports_reasoning_summaries":     true,
		"default_reasoning_summary":        "none",
		"support_verbosity":                false,
		"truncation_policy":                map[string]any{"mode": "bytes", "limit": 10000},
		"supports_parallel_tool_calls":     false,
		"supports_image_detail_original":   false,
		"context_window":                   contextWindow,
		"max_context_window":               contextWindow,
		"effective_context_window_percent": 95,
		"experimental_supported_tools":     []any{},
		"input_modalities":                 []any{"text"},
		"supports_search_tool":             false,
		"additional_speed_tiers":           []any{},
		"service_tiers":                    []any{},
		"availability_nux":                 nil,
		"upgrade":                          nil,
	}
}
