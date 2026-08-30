package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	providerMonitorConfigSetting = "provider_monitor_config_v1"
	providerMonitorStateSetting  = "provider_monitor_state_v1"

	providerMonitorInitialDelay       = 10 * time.Second
	providerMonitorRequestTimeout     = 12 * time.Second
	providerMonitorMaxConcurrency     = 4
	providerMonitorMaxRetainedAlerts  = 100
	providerMonitorMinIntervalMinutes = 15
	providerMonitorMaxIntervalMinutes = 7 * 24 * 60
)

type providerMonitorSettingStore interface {
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	SetSetting(ctx context.Context, key, value string) error
}

// ProviderMonitorConfig controls scheduled catalog refreshes and lightweight
// inference probes. ProbeModels remains in the persisted/API shape for
// compatibility, but normalization keeps it enabled so the monitor always
// checks both the model list and actual service availability.
type ProviderMonitorConfig struct {
	Enabled              bool `json:"enabled"`
	IntervalMinutes      int  `json:"interval_minutes"`
	ProbeModels          bool `json:"probe_models"`
	MaxModelsPerProvider int  `json:"max_models_per_provider"`
}

type ProviderModelHealth struct {
	Model      string    `json:"model"`
	State      string    `json:"state"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
}

type ProviderMonitorProviderStatus struct {
	ProviderID      string                `json:"provider_id"`
	ProviderName    string                `json:"provider_name"`
	State           string                `json:"state"`
	CatalogCount    int                   `json:"catalog_count"`
	CheckedModels   int                   `json:"checked_models"`
	HealthyModels   int                   `json:"healthy_models"`
	UnhealthyModels int                   `json:"unhealthy_models"`
	AddedModels     []string              `json:"added_models,omitempty"`
	RemovedModels   []string              `json:"removed_models,omitempty"`
	Models          []ProviderModelHealth `json:"models,omitempty"`
	Message         string                `json:"message,omitempty"`
	LastCheckedAt   time.Time             `json:"last_checked_at"`
}

type ProviderMonitorAlert struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Severity     string     `json:"severity"`
	ProviderID   string     `json:"provider_id"`
	ProviderName string     `json:"provider_name"`
	Model        string     `json:"model,omitempty"`
	Models       []string   `json:"models,omitempty"`
	Message      string     `json:"message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	LastSeenAt   time.Time  `json:"last_seen_at"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	Dismissed    bool       `json:"dismissed,omitempty"`
}

type ProviderMonitorSnapshot struct {
	Config    ProviderMonitorConfig           `json:"config"`
	Running   bool                            `json:"running"`
	LastRunAt time.Time                       `json:"last_run_at,omitempty"`
	NextRunAt time.Time                       `json:"next_run_at,omitempty"`
	Providers []ProviderMonitorProviderStatus `json:"providers"`
	Alerts    []ProviderMonitorAlert          `json:"alerts"`
}

type providerMonitorPersistedState struct {
	LastRunAt time.Time                                `json:"last_run_at,omitempty"`
	Providers map[string]ProviderMonitorProviderStatus `json:"providers,omitempty"`
	Alerts    []ProviderMonitorAlert                   `json:"alerts,omitempty"`
}

type providerMonitor struct {
	log      *slog.Logger
	store    providerMonitorSettingStore
	provider core.ProviderManager
	client   *http.Client

	runMu sync.Mutex
	mu    sync.Mutex

	config    ProviderMonitorConfig
	running   bool
	lastRunAt time.Time
	nextRunAt time.Time
	providers map[string]ProviderMonitorProviderStatus
	alerts    []ProviderMonitorAlert
	wake      chan struct{}
}

func defaultProviderMonitorConfig() ProviderMonitorConfig {
	return ProviderMonitorConfig{
		Enabled:              true,
		IntervalMinutes:      6 * 60,
		ProbeModels:          true,
		MaxModelsPerProvider: 20,
	}
}

func normalizeProviderMonitorConfig(cfg ProviderMonitorConfig) (ProviderMonitorConfig, error) {
	defaults := defaultProviderMonitorConfig()
	cfg.ProbeModels = true
	if cfg.IntervalMinutes == 0 {
		cfg.IntervalMinutes = defaults.IntervalMinutes
	}
	if cfg.MaxModelsPerProvider == 0 {
		cfg.MaxModelsPerProvider = defaults.MaxModelsPerProvider
	}
	if cfg.IntervalMinutes < providerMonitorMinIntervalMinutes || cfg.IntervalMinutes > providerMonitorMaxIntervalMinutes {
		return ProviderMonitorConfig{}, fmt.Errorf(
			"interval_minutes must be between %d and %d",
			providerMonitorMinIntervalMinutes,
			providerMonitorMaxIntervalMinutes,
		)
	}
	if cfg.MaxModelsPerProvider < 1 || cfg.MaxModelsPerProvider > 100 {
		return ProviderMonitorConfig{}, fmt.Errorf("max_models_per_provider must be between 1 and 100")
	}
	return cfg, nil
}

func newProviderMonitor(log *slog.Logger, st providerMonitorSettingStore, pm core.ProviderManager) *providerMonitor {
	if log == nil {
		log = slog.Default()
	}
	monitor := &providerMonitor{
		log:       log,
		store:     st,
		provider:  pm,
		client:    &http.Client{Timeout: providerMonitorRequestTimeout},
		config:    defaultProviderMonitorConfig(),
		providers: map[string]ProviderMonitorProviderStatus{},
		wake:      make(chan struct{}, 1),
	}
	monitor.load(context.Background())
	return monitor
}

func (m *providerMonitor) load(ctx context.Context) {
	if m == nil || m.store == nil {
		return
	}
	if raw, ok, err := m.store.GetSetting(ctx, providerMonitorConfigSetting); err == nil && ok {
		var cfg ProviderMonitorConfig
		if json.Unmarshal([]byte(raw), &cfg) == nil {
			if normalized, normalizeErr := normalizeProviderMonitorConfig(cfg); normalizeErr == nil {
				m.config = normalized
			}
		}
	}
	if raw, ok, err := m.store.GetSetting(ctx, providerMonitorStateSetting); err == nil && ok {
		var state providerMonitorPersistedState
		if json.Unmarshal([]byte(raw), &state) == nil {
			m.lastRunAt = state.LastRunAt
			if state.Providers != nil {
				m.providers = state.Providers
			}
			m.alerts = state.Alerts
		}
	}
}

func (m *providerMonitor) persistConfig(ctx context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("provider monitor store unavailable")
	}
	m.mu.Lock()
	raw, err := json.Marshal(m.config)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.store.SetSetting(ctx, providerMonitorConfigSetting, string(raw))
}

func (m *providerMonitor) persistState(ctx context.Context) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("provider monitor store unavailable")
	}
	m.mu.Lock()
	state := providerMonitorPersistedState{
		LastRunAt: m.lastRunAt,
		Providers: cloneProviderMonitorStatuses(m.providers),
		Alerts:    append([]ProviderMonitorAlert(nil), m.alerts...),
	}
	m.mu.Unlock()
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return m.store.SetSetting(ctx, providerMonitorStateSetting, string(raw))
}

func cloneProviderMonitorStatuses(source map[string]ProviderMonitorProviderStatus) map[string]ProviderMonitorProviderStatus {
	out := make(map[string]ProviderMonitorProviderStatus, len(source))
	for id, status := range source {
		status.AddedModels = append([]string(nil), status.AddedModels...)
		status.RemovedModels = append([]string(nil), status.RemovedModels...)
		status.Models = append([]ProviderModelHealth(nil), status.Models...)
		out[id] = status
	}
	return out
}

func (m *providerMonitor) Snapshot() ProviderMonitorSnapshot {
	if m == nil {
		return ProviderMonitorSnapshot{Config: defaultProviderMonitorConfig()}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := ProviderMonitorSnapshot{
		Config:    m.config,
		Running:   m.running,
		LastRunAt: m.lastRunAt,
		NextRunAt: m.nextRunAt,
		Providers: make([]ProviderMonitorProviderStatus, 0, len(m.providers)),
	}
	for _, status := range m.providers {
		status.AddedModels = append([]string(nil), status.AddedModels...)
		status.RemovedModels = append([]string(nil), status.RemovedModels...)
		status.Models = append([]ProviderModelHealth(nil), status.Models...)
		snapshot.Providers = append(snapshot.Providers, status)
	}
	sort.Slice(snapshot.Providers, func(i, j int) bool {
		return strings.ToLower(snapshot.Providers[i].ProviderName) < strings.ToLower(snapshot.Providers[j].ProviderName)
	})
	for _, alert := range m.alerts {
		if alert.Dismissed || alert.ResolvedAt != nil {
			continue
		}
		alert.Models = append([]string(nil), alert.Models...)
		snapshot.Alerts = append(snapshot.Alerts, alert)
	}
	sort.Slice(snapshot.Alerts, func(i, j int) bool {
		return snapshot.Alerts[i].CreatedAt.After(snapshot.Alerts[j].CreatedAt)
	})
	return snapshot
}

func (m *providerMonitor) UpdateConfig(ctx context.Context, cfg ProviderMonitorConfig) (ProviderMonitorSnapshot, error) {
	normalized, err := normalizeProviderMonitorConfig(cfg)
	if err != nil {
		return m.Snapshot(), err
	}
	m.mu.Lock()
	m.config = normalized
	if !normalized.Enabled {
		m.nextRunAt = time.Time{}
	}
	m.mu.Unlock()
	if err := m.persistConfig(ctx); err != nil {
		return m.Snapshot(), err
	}
	m.signalWake()
	return m.Snapshot(), nil
}

func (m *providerMonitor) DismissAlert(ctx context.Context, id string) (ProviderMonitorSnapshot, error) {
	m.mu.Lock()
	for index := range m.alerts {
		if id == "" || m.alerts[index].ID == id {
			m.alerts[index].Dismissed = true
		}
	}
	m.mu.Unlock()
	if err := m.persistState(ctx); err != nil {
		return m.Snapshot(), err
	}
	return m.Snapshot(), nil
}

func (m *providerMonitor) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *providerMonitor) Run(ctx context.Context) {
	if m == nil {
		return
	}
	for {
		m.mu.Lock()
		cfg := m.config
		lastRunAt := m.lastRunAt
		m.mu.Unlock()

		if !cfg.Enabled {
			select {
			case <-ctx.Done():
				return
			case <-m.wake:
				continue
			}
		}

		next := lastRunAt.Add(time.Duration(cfg.IntervalMinutes) * time.Minute)
		if lastRunAt.IsZero() {
			next = time.Now().Add(providerMonitorInitialDelay)
		}
		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}
		m.mu.Lock()
		m.nextRunAt = next
		m.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-m.wake:
			if !timer.Stop() {
				<-timer.C
			}
			continue
		case <-timer.C:
			if _, err := m.RunOnce(ctx); err != nil && ctx.Err() == nil {
				m.log.Warn("provider monitor run failed", "err", err)
			}
		}
	}
}

func (m *providerMonitor) RunOnce(ctx context.Context) (ProviderMonitorSnapshot, error) {
	return m.runOnce(ctx, false)
}

func (m *providerMonitor) RefreshCatalogs(ctx context.Context) (ProviderMonitorSnapshot, error) {
	return m.runOnce(ctx, true)
}

func (m *providerMonitor) runOnce(ctx context.Context, catalogOnly bool) (ProviderMonitorSnapshot, error) {
	if m == nil || m.provider == nil {
		return ProviderMonitorSnapshot{}, fmt.Errorf("provider monitor unavailable")
	}
	if !m.runMu.TryLock() {
		return m.Snapshot(), fmt.Errorf("provider monitor is already running")
	}
	defer m.runMu.Unlock()

	m.mu.Lock()
	m.running = true
	m.nextRunAt = time.Time{}
	cfg := m.config
	previousStatuses := cloneProviderMonitorStatuses(m.providers)
	m.mu.Unlock()
	if catalogOnly {
		cfg.ProbeModels = false
	}

	providers, err := m.provider.List(ctx)
	if err != nil {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		return m.Snapshot(), err
	}
	now := time.Now().UTC()
	statuses := make(map[string]ProviderMonitorProviderStatus, len(providers))
	seenProviders := make(map[string]bool, len(providers))
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		seenProviders[provider.ID] = true
		previous := previousStatuses[provider.ID]
		status, checkErr := m.checkProvider(ctx, cfg, provider, previous, now)
		if catalogOnly && checkErr == nil {
			status = preserveProviderModelHealth(status, previous)
		}
		statuses[provider.ID] = status
		if checkErr != nil {
			m.log.Warn("provider monitor check failed", "provider", provider.Name, "err", checkErr)
		}
	}

	m.mu.Lock()
	m.providers = statuses
	if !catalogOnly {
		m.lastRunAt = now
	}
	m.resolveMissingProviderAlertsLocked(seenProviders, now)
	m.trimAlertsLocked()
	m.mu.Unlock()
	persistErr := m.persistState(ctx)
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
	m.signalWake()
	return m.Snapshot(), persistErr
}

func preserveProviderModelHealth(refreshed, previous ProviderMonitorProviderStatus) ProviderMonitorProviderStatus {
	if previous.CheckedModels == 0 {
		return refreshed
	}
	refreshed.State = previous.State
	refreshed.CheckedModels = previous.CheckedModels
	refreshed.HealthyModels = previous.HealthyModels
	refreshed.UnhealthyModels = previous.UnhealthyModels
	refreshed.Models = append([]ProviderModelHealth(nil), previous.Models...)
	refreshed.Message = previous.Message
	refreshed.LastCheckedAt = previous.LastCheckedAt
	return refreshed
}

func (m *providerMonitor) checkProvider(
	ctx context.Context,
	cfg ProviderMonitorConfig,
	provider *core.Provider,
	previous ProviderMonitorProviderStatus,
	now time.Time,
) (ProviderMonitorProviderStatus, error) {
	status := ProviderMonitorProviderStatus{
		ProviderID:    provider.ID,
		ProviderName:  provider.Name,
		State:         "checking",
		LastCheckedAt: now,
	}
	if strings.TrimSpace(provider.BaseURL) == "" {
		status.State = "skipped"
		status.Message = "provider has no remote model endpoint"
		m.resolveProviderHealthAlerts(provider.ID, nil, now)
		return status, nil
	}
	if err := normalizeProviderAPIKey(provider); err != nil {
		status.State = "error"
		status.Message = err.Error()
		m.setCatalogErrorAlert(provider, status.Message, now)
		return status, err
	}
	apiKey := os.Getenv(strings.TrimSpace(provider.APIKeyEnv))
	if apiKey == "" {
		status.State = "error"
		status.Message = fmt.Sprintf("API key %s is not available", provider.APIKeyEnv)
		m.setCatalogErrorAlert(provider, status.Message, now)
		return status, fmt.Errorf("%s", status.Message)
	}

	models, err := discoverProviderCatalog(ctx, m.client, provider, apiKey)
	if err != nil {
		status.State = "error"
		status.Message = err.Error()
		m.setCatalogErrorAlert(provider, status.Message, now)
		return status, err
	}
	status.CatalogCount = len(models)

	// Re-read before saving so an operator edit made while the remote request
	// was in flight is preserved. The monitor owns supported_models and replaces
	// a default model only when a definitive availability check took it offline.
	latest, err := m.provider.Get(ctx, provider.ID)
	if err != nil {
		status.State = "error"
		status.Message = fmt.Sprintf("reload provider before catalog save: %v", err)
		m.setCatalogErrorAlert(provider, status.Message, now)
		return status, err
	}
	if latest != nil {
		provider = latest
	}

	if !cfg.ProbeModels {
		availableModels := availableProviderModels(models, nil, previous.Models)
		if err := m.saveAvailableProviderModels(ctx, provider, availableModels, nil, previous.Models, &status, now); err != nil {
			return status, err
		}
		status.State = "skipped"
		status.Message = "model catalog refreshed; availability was not checked"
		return status, nil
	}

	modelsToCheck := prioritizeProviderModels(provider.Model, models, cfg.MaxModelsPerProvider)
	status.Models = m.checkProviderModels(ctx, provider, apiKey, modelsToCheck, now)
	status.CheckedModels = len(status.Models)
	seenErrorIDs := make(map[string]bool)
	for _, model := range status.Models {
		if model.State == "healthy" {
			status.HealthyModels++
			continue
		}
		status.UnhealthyModels++
		alertID := providerMonitorHealthAlertID("model_error", provider.ID, model.Model)
		seenErrorIDs[alertID] = true
		m.setModelErrorAlert(provider, model, now)
	}
	m.resolveProviderHealthAlerts(provider.ID, seenErrorIDs, now)
	availableModels := availableProviderModels(models, status.Models, previous.Models)
	if err := m.saveAvailableProviderModels(ctx, provider, availableModels, status.Models, previous.Models, &status, now); err != nil {
		return status, err
	}

	switch {
	case status.CheckedModels == 0:
		status.State = "warning"
		status.Message = "no models were checked"
	case status.UnhealthyModels == 0:
		status.State = "healthy"
	case status.HealthyModels == 0:
		status.State = "error"
		status.Message = "all checked models failed"
	default:
		status.State = "warning"
		status.Message = fmt.Sprintf("%d of %d checked models failed", status.UnhealthyModels, status.CheckedModels)
	}
	return status, nil
}

// availableProviderModels keeps the remote catalog as the discovery source,
// but removes models whose inference endpoint definitively says they do not
// exist. A successful later probe adds the model back automatically. Transient
// failures such as timeouts, rate limits, and 5xx responses remain selectable.
func availableProviderModels(catalog []string, current, previous []ProviderModelHealth) []string {
	currentByModel := providerModelHealthByName(current)
	previousByModel := providerModelHealthByName(previous)
	available := make([]string, 0, len(catalog))
	for _, model := range catalog {
		health, ok := currentByModel[model]
		if !ok {
			health, ok = previousByModel[model]
		}
		if ok && shouldAutoOfflineProviderModel(health) {
			continue
		}
		available = append(available, model)
	}
	return available
}

func providerModelHealthByName(models []ProviderModelHealth) map[string]ProviderModelHealth {
	byName := make(map[string]ProviderModelHealth, len(models))
	for _, health := range models {
		if model := strings.TrimSpace(health.Model); model != "" {
			byName[model] = health
		}
	}
	return byName
}

func shouldAutoOfflineProviderModel(health ProviderModelHealth) bool {
	if health.State == "healthy" {
		return false
	}
	return health.StatusCode == http.StatusNotFound || health.StatusCode == http.StatusGone
}

func providerModelWasAutoOfflined(model string, current, previous []ProviderModelHealth) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if health, ok := providerModelHealthByName(current)[model]; ok {
		return shouldAutoOfflineProviderModel(health)
	}
	health, ok := providerModelHealthByName(previous)[model]
	return ok && shouldAutoOfflineProviderModel(health)
}

func (m *providerMonitor) saveAvailableProviderModels(
	ctx context.Context,
	provider *core.Provider,
	models []string,
	currentHealth, previousHealth []ProviderModelHealth,
	status *ProviderMonitorProviderStatus,
	now time.Time,
) error {
	status.AddedModels, status.RemovedModels = diffProviderModels(provider.Meta.SupportedModels, models)
	provider.Meta.SupportedModels = append([]string(nil), models...)
	if strings.TrimSpace(provider.Model) == "" || providerModelWasAutoOfflined(provider.Model, currentHealth, previousHealth) {
		provider.Model = ""
		if len(models) > 0 {
			provider.Model = models[0]
		}
	}
	if err := m.provider.Upsert(ctx, provider); err != nil {
		status.State = "error"
		status.Message = fmt.Sprintf("save refreshed catalog: %v", err)
		m.setCatalogErrorAlert(provider, status.Message, now)
		return err
	}

	m.mu.Lock()
	m.resolveHealthAlertLocked(providerMonitorHealthAlertID("catalog_error", provider.ID, ""), now)
	if len(status.AddedModels) > 0 {
		m.addEventAlertLocked("new_models", "info", provider, "", status.AddedModels, "", now)
	}
	if len(status.RemovedModels) > 0 {
		m.addEventAlertLocked("removed_models", "warning", provider, "", status.RemovedModels, "", now)
	}
	m.mu.Unlock()
	return nil
}

func discoverProviderCatalog(ctx context.Context, client *http.Client, provider *core.Provider, apiKey string) ([]string, error) {
	apiFormat := providerMonitorAPIFormat(provider)
	if apiFormat == "gemini_native" {
		return discoverGeminiProviderCatalog(ctx, client, provider.BaseURL, apiKey)
	}
	var lastErr error
	for _, candidateFormat := range orderedProbeAPIFormats(apiFormat) {
		for _, endpoint := range candidateModelURLs(provider.BaseURL, candidateFormat) {
			models, status, body, err := fetchProviderModels(ctx, client, endpoint, apiKey, candidateFormat)
			if err != nil {
				lastErr = err
				continue
			}
			if status < 200 || status >= 300 {
				lastErr = fmt.Errorf("%s returned HTTP %d: %s", endpoint, status, providerMonitorResponseMessage(body))
				continue
			}
			if len(models) == 0 {
				lastErr = fmt.Errorf("%s returned no models", endpoint)
				continue
			}
			sort.Strings(models)
			return uniqueProviderModels(models), nil
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no model endpoint candidates for %s", provider.BaseURL)
	}
	return nil, lastErr
}

func providerMonitorAPIFormat(provider *core.Provider) string {
	if provider == nil {
		return "anthropic"
	}
	format := strings.TrimSpace(provider.Meta.APIFormat)
	if format == "gemini" {
		return "gemini_native"
	}
	if format != "" {
		return format
	}
	if parsed, err := url.Parse(strings.TrimSpace(provider.BaseURL)); err == nil &&
		strings.Contains(strings.ToLower(parsed.Hostname()), "generativelanguage.googleapis.com") {
		return "gemini_native"
	}
	return "anthropic"
}

func discoverGeminiProviderCatalog(ctx context.Context, client *http.Client, baseURL, apiKey string) ([]string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid request URL")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/models")
	if !strings.HasSuffix(path, "/v1beta") {
		path += "/v1beta"
	}
	parsed.Path = path + "/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("x-goog-api-key", apiKey)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned HTTP %d: %s", parsed.String(), response.StatusCode, providerMonitorResponseMessage(string(body)))
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		if name := strings.TrimPrefix(strings.TrimSpace(model.Name), "models/"); name != "" {
			models = append(models, name)
		}
	}
	models = uniqueProviderModels(models)
	sort.Strings(models)
	if len(models) == 0 {
		return nil, fmt.Errorf("%s returned no models", parsed.String())
	}
	return models, nil
}

func uniqueProviderModels(models []string) []string {
	seen := make(map[string]bool, len(models))
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		out = append(out, model)
	}
	return out
}

func diffProviderModels(previous, current []string) (added, removed []string) {
	previousSet := make(map[string]bool, len(previous))
	currentSet := make(map[string]bool, len(current))
	for _, model := range previous {
		model = strings.TrimSpace(model)
		if model != "" {
			previousSet[model] = true
		}
	}
	for _, model := range current {
		model = strings.TrimSpace(model)
		if model != "" {
			currentSet[model] = true
		}
	}
	for model := range currentSet {
		if !previousSet[model] {
			added = append(added, model)
		}
	}
	for model := range previousSet {
		if !currentSet[model] {
			removed = append(removed, model)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

func prioritizeProviderModels(defaultModel string, models []string, limit int) []string {
	ordered := make([]string, 0, len(models))
	seen := map[string]bool{}
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			return
		}
		seen[model] = true
		ordered = append(ordered, model)
	}
	add(defaultModel)
	for _, model := range models {
		add(model)
	}
	if limit > 0 && len(ordered) > limit {
		ordered = ordered[:limit]
	}
	return ordered
}

func (m *providerMonitor) checkProviderModels(
	ctx context.Context,
	provider *core.Provider,
	apiKey string,
	models []string,
	now time.Time,
) []ProviderModelHealth {
	results := make([]ProviderModelHealth, len(models))
	semaphore := make(chan struct{}, providerMonitorMaxConcurrency)
	var wg sync.WaitGroup
	for index, model := range models {
		index, model := index, model
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[index] = ProviderModelHealth{
					Model: model, State: "unavailable", Message: ctx.Err().Error(), CheckedAt: now,
				}
				return
			}
			results[index] = probeProviderModel(ctx, m.client, provider, apiKey, model, now)
		}()
	}
	wg.Wait()
	return results
}

func probeProviderModel(
	ctx context.Context,
	client *http.Client,
	provider *core.Provider,
	apiKey, model string,
	now time.Time,
) ProviderModelHealth {
	result := ProviderModelHealth{Model: model, State: "unavailable", CheckedAt: now}
	endpoint, protocol, err := providerModelProbeEndpoint(provider.BaseURL, providerMonitorAPIFormat(provider), model)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	payload := map[string]any{"model": model, "stream": false}
	switch protocol {
	case "anthropic":
		payload["messages"] = []map[string]string{{"role": "user", "content": "ping"}}
		payload["max_tokens"] = 1
	case "openai_responses":
		payload["input"] = "ping"
		payload["max_output_tokens"] = 1
	case "openai_chat":
		payload["messages"] = []map[string]string{{"role": "user", "content": "ping"}}
		payload["max_tokens"] = 1
	case "gemini_native":
		payload["contents"] = []map[string]any{{
			"role":  "user",
			"parts": []map[string]string{{"text": "ping"}},
		}}
		payload["generationConfig"] = map[string]int{"maxOutputTokens": 1}
	default:
		result.Message = fmt.Sprintf("model probes are not supported for %s", protocol)
		return result
	}
	data, err := json.Marshal(payload)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	requestCtx, cancel := context.WithTimeout(ctx, providerMonitorRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		result.Message = err.Error()
		return result
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if protocol == "anthropic" {
		request.Header.Set("x-api-key", apiKey)
		request.Header.Set("anthropic-version", "2023-06-01")
	} else if protocol == "gemini_native" {
		request.Header.Set("x-goog-api-key", apiKey)
	} else {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(request)
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer response.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 256<<10))
	result.StatusCode = response.StatusCode
	if readErr != nil {
		result.Message = readErr.Error()
		return result
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		result.State = "healthy"
		result.Message = "OK"
		return result
	}
	result.Message = fmt.Sprintf("HTTP %d", response.StatusCode)
	if message := providerMonitorResponseMessage(string(body)); message != "" {
		result.Message += ": " + message
	}
	return result
}

func providerModelProbeEndpoint(baseURL, apiFormat, model string) (endpoint, protocol string, err error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", "", fmt.Errorf("invalid request URL")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	path = strings.TrimSuffix(path, "/models")
	if !strings.HasSuffix(path, "/v1") {
		path += "/v1"
	}
	switch apiFormat {
	case "", "anthropic":
		path += "/messages"
		protocol = "anthropic"
	case "openai_responses":
		path += "/responses"
		protocol = "openai_responses"
	case "openai_chat":
		path += "/chat/completions"
		protocol = "openai_chat"
	case "gemini_native":
		path = strings.TrimSuffix(path, "/v1")
		if !strings.HasSuffix(path, "/v1beta") {
			path += "/v1beta"
		}
		path += "/models/" + strings.TrimPrefix(model, "models/") + ":generateContent"
		protocol = "gemini_native"
	default:
		return "", "", fmt.Errorf("model probes are not supported for API format %q", apiFormat)
	}
	parsed.Path = path
	return parsed.String(), protocol, nil
}

func providerMonitorResponseMessage(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal([]byte(body), &payload) == nil {
		if message, ok := payload["message"].(string); ok {
			return truncateProviderMonitorMessage(message)
		}
		if errorValue, ok := payload["error"]; ok {
			switch value := errorValue.(type) {
			case string:
				return truncateProviderMonitorMessage(value)
			case map[string]any:
				if message, ok := value["message"].(string); ok {
					return truncateProviderMonitorMessage(message)
				}
				if kind, ok := value["type"].(string); ok {
					return truncateProviderMonitorMessage(kind)
				}
			}
		}
	}
	return truncateProviderMonitorMessage(body)
}

func truncateProviderMonitorMessage(message string) string {
	message = strings.Join(strings.Fields(message), " ")
	const limit = 240
	if len(message) <= limit {
		return message
	}
	return message[:limit] + "…"
}

func (m *providerMonitor) setCatalogErrorAlert(provider *core.Provider, message string, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertHealthAlertLocked("catalog_error", "error", provider, "", message, now)
}

func (m *providerMonitor) setModelErrorAlert(provider *core.Provider, health ProviderModelHealth, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.upsertHealthAlertLocked("model_error", "error", provider, health.Model, health.Message, now)
}

func (m *providerMonitor) upsertHealthAlertLocked(
	kind, severity string,
	provider *core.Provider,
	model, message string,
	now time.Time,
) {
	id := providerMonitorHealthAlertID(kind, provider.ID, model)
	for index := range m.alerts {
		if m.alerts[index].ID != id {
			continue
		}
		wasResolved := m.alerts[index].ResolvedAt != nil
		m.alerts[index].Severity = severity
		m.alerts[index].ProviderName = provider.Name
		m.alerts[index].Message = message
		m.alerts[index].LastSeenAt = now
		m.alerts[index].ResolvedAt = nil
		if wasResolved {
			m.alerts[index].CreatedAt = now
			m.alerts[index].Dismissed = false
		}
		return
	}
	m.alerts = append(m.alerts, ProviderMonitorAlert{
		ID:           id,
		Type:         kind,
		Severity:     severity,
		ProviderID:   provider.ID,
		ProviderName: provider.Name,
		Model:        model,
		Message:      message,
		CreatedAt:    now,
		LastSeenAt:   now,
	})
}

func (m *providerMonitor) addEventAlertLocked(
	kind, severity string,
	provider *core.Provider,
	model string,
	models []string,
	message string,
	now time.Time,
) {
	key := strings.Join(append([]string{kind, provider.ID, model}, models...), "\x00")
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", key, now.UnixNano())))
	m.alerts = append(m.alerts, ProviderMonitorAlert{
		ID:           "provider_event:" + hex.EncodeToString(sum[:8]),
		Type:         kind,
		Severity:     severity,
		ProviderID:   provider.ID,
		ProviderName: provider.Name,
		Model:        model,
		Models:       append([]string(nil), models...),
		Message:      message,
		CreatedAt:    now,
		LastSeenAt:   now,
	})
}

func providerMonitorHealthAlertID(kind, providerID, model string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{kind, providerID, model}, "\x00")))
	return "provider_health:" + hex.EncodeToString(sum[:8])
}

func (m *providerMonitor) resolveProviderHealthAlerts(providerID string, seen map[string]bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range m.alerts {
		alert := &m.alerts[index]
		if alert.ProviderID != providerID || alert.ResolvedAt != nil {
			continue
		}
		if alert.Type != "catalog_error" && alert.Type != "model_error" {
			continue
		}
		if seen != nil && seen[alert.ID] {
			continue
		}
		alert.ResolvedAt = &now
	}
}

func (m *providerMonitor) resolveHealthAlertLocked(id string, now time.Time) {
	for index := range m.alerts {
		if m.alerts[index].ID == id && m.alerts[index].ResolvedAt == nil {
			m.alerts[index].ResolvedAt = &now
		}
	}
}

func (m *providerMonitor) resolveMissingProviderAlertsLocked(seenProviders map[string]bool, now time.Time) {
	for index := range m.alerts {
		alert := &m.alerts[index]
		if alert.ResolvedAt != nil || seenProviders[alert.ProviderID] {
			continue
		}
		if alert.Type == "catalog_error" || alert.Type == "model_error" {
			alert.ResolvedAt = &now
		}
	}
}

func (m *providerMonitor) trimAlertsLocked() {
	if len(m.alerts) <= providerMonitorMaxRetainedAlerts {
		return
	}
	sort.SliceStable(m.alerts, func(i, j int) bool {
		return m.alerts[i].LastSeenAt.After(m.alerts[j].LastSeenAt)
	})
	m.alerts = m.alerts[:providerMonitorMaxRetainedAlerts]
}

func (s *Server) handleProviderMonitorGet(w http.ResponseWriter, _ *http.Request) {
	if s.providerMonitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider monitor unavailable")
		return
	}
	writeJSON(w, http.StatusOK, s.providerMonitor.Snapshot())
}

func (s *Server) handleProviderMonitorPut(w http.ResponseWriter, r *http.Request) {
	if s.providerMonitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider monitor unavailable")
		return
	}
	var cfg ProviderMonitorConfig
	if !decodeJSONInto(w, r, &cfg) {
		return
	}
	snapshot, err := s.providerMonitor.UpdateConfig(r.Context(), cfg)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleProviderMonitorRun(w http.ResponseWriter, r *http.Request) {
	if s.providerMonitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider monitor unavailable")
		return
	}
	var snapshot ProviderMonitorSnapshot
	var err error
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("catalog_only")), "true") {
		snapshot, err = s.providerMonitor.RefreshCatalogs(r.Context())
	} else {
		snapshot, err = s.providerMonitor.RunOnce(r.Context())
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error":    err.Error(),
			"snapshot": snapshot,
		})
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleProviderMonitorAlertDismiss(w http.ResponseWriter, r *http.Request) {
	if s.providerMonitor == nil {
		writeErr(w, http.StatusServiceUnavailable, "provider monitor unavailable")
		return
	}
	snapshot, err := s.providerMonitor.DismissAlert(r.Context(), strings.TrimSpace(r.URL.Query().Get("id")))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}
