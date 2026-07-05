package server

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/agentnexus/agentnexus/core"
)

const providerAPIKeyEnvPrefix = "AGENTNEXUS_PROVIDER_"

var providerAPIKeyEnvNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func normalizeProviderAPIKey(p *core.Provider) error {
	if p == nil {
		return nil
	}
	apiKey := strings.TrimSpace(p.APIKey)
	apiKeyEnv := strings.TrimSpace(p.APIKeyEnv)
	p.APIKey = ""

	if apiKey == "" && apiKeyEnv != "" && looksLikeInlineAPIKey(apiKeyEnv) {
		apiKey = apiKeyEnv
		apiKeyEnv = ""
	}
	if apiKey == "" {
		p.APIKeyEnv = apiKeyEnv
		return nil
	}
	if apiKeyEnv == "" || !providerAPIKeyEnvNameRe.MatchString(apiKeyEnv) {
		apiKeyEnv = providerAPIKeyEnvName(p)
	}
	if err := os.Setenv(apiKeyEnv, apiKey); err != nil {
		return fmt.Errorf("inject API key env: %w", err)
	}
	p.APIKeyEnv = apiKeyEnv
	return nil
}

func providerAPIKeyEnvName(p *core.Provider) string {
	source := strings.TrimSpace(p.ID)
	if source == "" {
		source = strings.TrimSpace(p.Name)
	}
	if source == "" {
		source = "CUSTOM"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range source {
		var out rune
		switch {
		case r >= 'a' && r <= 'z':
			out = r - 'a' + 'A'
		case r >= 'A' && r <= 'Z':
			out = r
		case r >= '0' && r <= '9':
			out = r
		default:
			out = '_'
		}
		if out == '_' {
			if lastUnderscore {
				continue
			}
			lastUnderscore = true
		} else {
			lastUnderscore = false
		}
		b.WriteRune(out)
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		slug = "CUSTOM"
	}
	return providerAPIKeyEnvPrefix + slug + "_API_KEY"
}

func looksLikeInlineAPIKey(value string) bool {
	if !providerAPIKeyEnvNameRe.MatchString(value) {
		return true
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"sk_", "sk-", "plat_", "ak_", "rk_"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
