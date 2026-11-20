// Package pricing prices token usage using a LiteLLM-derived table, cached
// locally for 24h, with hardcoded fallbacks for common Claude/GPT models so
// fuzzy matching never misprices known models.
package pricing

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// litellmURL is the upstream pricing map.
const litellmURL = "https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"

// modelPrice holds per-token USD prices.
type modelPrice struct {
	Input      float64 `json:"input_cost_per_token"`
	Output     float64 `json:"output_cost_per_token"`
	CacheRead  float64 `json:"cache_read_input_token_cost"`
	CacheWrite float64 `json:"cache_creation_input_token_cost"`
	tiers      []contextPrice
}

type contextPrice struct {
	above int64
	price modelPrice
}

// UnmarshalJSON also reads LiteLLM's context tiers. Only standard token rates
// are selected; suffixes such as _priority and _flex describe separate tiers
// that cannot be inferred from a transcript's token counts.
func (p *modelPrice) UnmarshalJSON(data []byte) error {
	type basePrice modelPrice
	var base basePrice
	if err := json.Unmarshal(data, &base); err != nil {
		return err
	}
	*p = modelPrice(base)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	thresholds := map[int64]bool{}
	for key := range fields {
		_, suffix, ok := strings.Cut(key, "_above_")
		if !ok || !strings.HasSuffix(suffix, "k_tokens") {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSuffix(suffix, "k_tokens"), 10, 64)
		if err == nil && n > 0 {
			thresholds[n*1000] = true
		}
	}
	for threshold := range thresholds {
		tier := *p
		tier.tiers = nil
		suffix := "_above_" + strconv.FormatInt(threshold/1000, 10) + "k_tokens"
		for key, dest := range map[string]*float64{
			"input_cost_per_token": &tier.Input, "output_cost_per_token": &tier.Output,
			"cache_read_input_token_cost": &tier.CacheRead, "cache_creation_input_token_cost": &tier.CacheWrite,
		} {
			if raw, ok := fields[key+suffix]; ok && string(raw) != "null" {
				if err := json.Unmarshal(raw, dest); err != nil {
					return err
				}
			}
		}
		p.tiers = append(p.tiers, contextPrice{above: threshold, price: tier})
	}
	sort.Slice(p.tiers, func(i, j int) bool { return p.tiers[i].above < p.tiers[j].above })
	return nil
}

// Pricer computes costs from token counts.
type Pricer struct {
	cacheDir string
	offline  bool
	mu       sync.Mutex
	table    map[string]modelPrice
	loaded   bool
	// resolved remembers the price chosen for each model name. A report prices
	// every record, and a name that is not an exact table key otherwise costs a
	// scan of the whole table (thousands of entries) per record.
	resolved   map[string]modelPrice
	checkedAt  time.Time
	refreshing bool
}

const (
	priceCacheFile = "litellm-prices.json"
	priceCacheTTL  = 24 * time.Hour
	// A long-running daemon looks at the cache file's age this often.
	priceRecheckInterval = time.Hour
	maxResolvedModels    = 4096
)

// New builds a Pricer. cacheDir defaults to ~/.cache/agentmux.
func New(cacheDir string, offline bool) *Pricer {
	if cacheDir == "" {
		home, _ := os.UserHomeDir()
		cacheDir = filepath.Join(home, ".cache", "agentmux")
	}
	return &Pricer{cacheDir: cacheDir, offline: offline}
}

// Cost returns the USD cost for the given token counts under model.
func (p *Pricer) Cost(model string, in, out, cacheRead, cacheWrite int64) float64 {
	mp := p.price(model)
	// Normalized input excludes cache reads/writes; all three contribute to
	// request prompt length. Output tokens do not select a context tier.
	promptTokens := in + cacheRead + cacheWrite
	for _, tier := range mp.tiers {
		if promptTokens > tier.above {
			mp = tier.price
		}
	}
	return float64(in)*mp.Input + float64(out)*mp.Output +
		float64(cacheRead)*mp.CacheRead + float64(cacheWrite)*mp.CacheWrite
}

func (p *Pricer) price(model string) modelPrice {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ensureLocked()
	if mp, ok := p.resolved[model]; ok {
		return mp
	}
	mp, ok := p.lookup(model)
	if !ok {
		mp = fallback(model)
	}
	if p.resolved == nil || len(p.resolved) >= maxResolvedModels {
		p.resolved = map[string]modelPrice{}
	}
	p.resolved[model] = mp
	return mp
}

// ensureLocked loads the price table once and keeps it current. A cache file
// past its TTL is still used: prices that are a few days old are closer to the
// truth than the built-in fallbacks, and the refresh runs in the background so
// a report never waits on the network.
func (p *Pricer) ensureLocked() {
	cachePath := filepath.Join(p.cacheDir, priceCacheFile)
	if !p.loaded {
		p.loaded = true
		p.checkedAt = time.Now()
		p.table = map[string]modelPrice{}
		if data, err := os.ReadFile(cachePath); err == nil {
			_ = json.Unmarshal(data, &p.table)
		}
		if p.offline {
			return
		}
		if len(p.table) == 0 {
			// First run on this machine: there is nothing to price with yet.
			if table := fetchTable(cachePath, p.cacheDir); len(table) > 0 {
				p.table = table
			}
			return
		}
		if !fresh(cachePath) {
			p.refreshLocked(cachePath)
		}
		return
	}
	if p.offline || p.refreshing || time.Since(p.checkedAt) < priceRecheckInterval {
		return
	}
	p.checkedAt = time.Now()
	if !fresh(cachePath) {
		p.refreshLocked(cachePath)
	}
}

func (p *Pricer) refreshLocked(cachePath string) {
	p.refreshing = true
	go func() {
		table := fetchTable(cachePath, p.cacheDir)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.refreshing = false
		if len(table) > 0 {
			p.table = table
			p.resolved = nil
		}
	}()
}

// fetchTable downloads the price table and stores it only when it parses, so
// a truncated download never replaces a usable cache file.
func fetchTable(cachePath, cacheDir string) map[string]modelPrice {
	data := fetch()
	if data == nil {
		return nil
	}
	table := map[string]modelPrice{}
	if err := json.Unmarshal(data, &table); err != nil || len(table) == 0 {
		return nil
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, cachePath)
	}
	return table
}

// lookup finds the table entry for model: an exact key, else the longest key
// contained in the name (so "vendor/gpt-x" prices as "gpt-x"). Equal-length
// candidates resolve to the alphabetically first key, so the same model never
// changes price between reports.
func (p *Pricer) lookup(model string) (modelPrice, bool) {
	if mp, ok := p.table[model]; ok {
		return mp, true
	}
	lower := strings.ToLower(model)
	var best modelPrice
	bestKey := ""
	for k, v := range p.table {
		key := strings.ToLower(k)
		if len(key) < len(bestKey) || !strings.Contains(lower, key) {
			continue
		}
		if len(key) > len(bestKey) || key < bestKey {
			best, bestKey = v, key
		}
	}
	return best, bestKey != ""
}

func fresh(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < priceCacheTTL
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
