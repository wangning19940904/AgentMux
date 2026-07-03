// Package pricing prices token usage using a LiteLLM-derived table, cached
// locally for 24h, with hardcoded fallbacks for common Claude/GPT models so
// fuzzy matching never misprices known models.
package pricing

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// litellmURL is the upstream pricing map.
const litellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// modelPrice holds per-million-token USD prices.
type modelPrice struct {
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheRead  float64 `json:"cache_read_input_token_cost"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
}

// Pricer computes costs from token counts.
type Pricer struct {
	cacheDir string
	offline  bool
	mu       sync.Mutex
	table    map[string]modelPrice
	loaded   bool
}

// New builds a Pricer. cacheDir defaults to ~/.cache/agentnexus.
func New(cacheDir string, offline bool) *Pricer {
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache", "agentnexus")
	}
	return &Pricer{cacheDir: cacheDir, offline: offline}
}

// Cost returns the USD cost for the given token counts under model.
func (p *Pricer) Cost(model string, in, out, cacheRead, cacheWrite int64) float64 {
	p.ensure()
	mp, ok := p.lookup(model)
	if !ok {
		mp = fallback(model)
	}
	return float64(in)*mp.Input + float64(out)*mp.Output +
		float64(cacheRead)*mp.CacheRead + float64(cacheWrite)*mp.CacheWrite
}

func (p *Pricer) ensure() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loaded {
		return
	}
	p.loaded = true
	p.table = map[string]modelPrice{}
	cachePath := filepath.Join(p.cacheDir, "litellm-prices.json")
	if data, err := os.ReadFile(cachePath); err == nil && fresh(cachePath) {
		_ = json.Unmarshal(data, &p.table)
		if len(p.table) > 0 {
			return
		}
	}
	if p.offline {
		return
	}
	if data := fetch(); data != nil {
		_ = json.Unmarshal(data, &p.table)
		_ = os.MkdirAll(p.cacheDir, 0o755)
		_ = os.WriteFile(cachePath, data, 0o644)
	}
}

func (p *Pricer) lookup(model string) (modelPrice, bool) {
	if mp, ok := p.table[model]; ok {
		return mp, true
	}
	lower := strings.ToLower(model)
	for k, v := range p.table {
		if strings.Contains(lower, strings.ToLower(k)) {
			return v, true
		}
	}
	return modelPrice{}, false
}

func fresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < 24*time.Hour
}

func fetch() []byte {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(litellmURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	buf := make([]byte, 0, 1<<20)
	tmp := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf
}
