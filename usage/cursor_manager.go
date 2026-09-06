package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

const (
	cursorUsageStateKey     = "usage:cursor:state"
	cursorBackfillWindow    = 90 * 24 * time.Hour
	cursorCloudOverlap      = 2 * time.Hour
	cursorCloudPollInterval = time.Hour
	cursorLocalPollInterval = 30 * time.Second
)

type CursorUsageSourceStatus struct {
	Source             string           `json:"source"`
	Connected          bool             `json:"connected"`
	Syncing            bool             `json:"syncing"`
	Scope              string           `json:"scope"`
	BackfillDays       int              `json:"backfill_days"`
	AllowEstimates     bool             `json:"allow_estimates"`
	Hook               cursorHookStatus `json:"hook"`
	LocalDatabase      string           `json:"local_database"`
	LocalStatus        string           `json:"local_status"`
	CloudStatus        string           `json:"cloud_status"`
	BackfillComplete   bool             `json:"backfill_complete"`
	BackfillPage       int              `json:"backfill_page,omitempty"`
	LastSyncAt         string           `json:"last_sync_at,omitempty"`
	LastError          string           `json:"last_error,omitempty"`
	LocalRecords       int              `json:"local_records"`
	CloudMatchedEvents int              `json:"cloud_matched_events"`
	CloudIgnoredEvents int              `json:"cloud_ignored_events"`
	EstimatedTokens    int64            `json:"estimated_tokens"`
	TotalTokens        int64            `json:"total_tokens"`
}

type CursorUsageActionResult struct {
	OK      bool                    `json:"ok"`
	Action  string                  `json:"action"`
	Message string                  `json:"message"`
	Started bool                    `json:"started,omitempty"`
	Status  CursorUsageSourceStatus `json:"status"`
}

type cursorUsageState struct {
	Connected          bool   `json:"connected"`
	LocalRowID         int64  `json:"local_row_id"`
	BackfillFrom       string `json:"backfill_from,omitempty"`
	BackfillPage       int    `json:"backfill_page,omitempty"`
	BackfillComplete   bool   `json:"backfill_complete"`
	LastSyncAt         string `json:"last_sync_at,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	LocalStatus        string `json:"local_status,omitempty"`
	CloudStatus        string `json:"cloud_status,omitempty"`
	LocalRecords       int    `json:"local_records"`
	CloudMatchedEvents int    `json:"cloud_matched_events"`
	CloudIgnoredEvents int    `json:"cloud_ignored_events"`
}

type cursorRecordBatch func(context.Context, []core.UsageRecord) error
type cursorSyncFunc func(context.Context, bool) error

type CursorUsageManager struct {
	store       *store.Store
	log         *slog.Logger
	home        string
	dbPath      string
	http        *http.Client
	recordBatch cursorRecordBatch

	mu           sync.Mutex
	state        cursorUsageState
	syncing      bool
	pendingCloud bool
	runCtx       context.Context
	syncMu       sync.Mutex
	syncFn       cursorSyncFunc
}

func NewCursorUsageManager(st *store.Store, log *slog.Logger, home string, recordBatch cursorRecordBatch) *CursorUsageManager {
	if log == nil {
		log = slog.Default()
	}
	manager := &CursorUsageManager{
		store: st, log: log, home: home, dbPath: cursorStateDBPath(home), http: newCursorHTTPClient(), recordBatch: recordBatch,
	}
	manager.loadState(context.Background())
	return manager
}

func (m *CursorUsageManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.runCtx = ctx
	connected := m.state.Connected
	m.mu.Unlock()
	if connected {
		m.triggerSync(true)
	}
	localTicker := time.NewTicker(cursorLocalPollInterval)
	cloudTicker := time.NewTicker(cursorCloudPollInterval)
	defer localTicker.Stop()
	defer cloudTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-localTicker.C:
			m.triggerSync(false)
		case <-cloudTicker.C:
			m.triggerSync(true)
		}
	}
}

func (m *CursorUsageManager) Status(ctx context.Context) CursorUsageSourceStatus {
	if m == nil {
		return CursorUsageSourceStatus{Source: "cursor", Scope: "agent", BackfillDays: 90, AllowEstimates: true}
	}
	m.mu.Lock()
	state := m.state
	syncing := m.syncing
	m.mu.Unlock()
	localDatabase := m.dbPath
	if !fileExists(localDatabase) && cursorCLIAvailable(m.home) {
		localDatabase = cursorCLIChatsRoot(m.home)
	}
	status := CursorUsageSourceStatus{
		Source: "cursor", Connected: state.Connected, Syncing: syncing, Scope: "agent", BackfillDays: 90, AllowEstimates: true,
		Hook: inspectCursorHook(m.home), LocalDatabase: localDatabase, LocalStatus: firstCursorValue(state.LocalStatus, "not_connected"),
		CloudStatus: firstCursorValue(state.CloudStatus, "not_connected"), BackfillComplete: state.BackfillComplete,
		BackfillPage: state.BackfillPage, LastSyncAt: state.LastSyncAt, LastError: state.LastError,
		CloudIgnoredEvents: state.CloudIgnoredEvents,
	}
	if sessions, err := discoverCursorCLISessions(ctx, m.home, time.Now().UTC().Add(-cursorBackfillWindow)); err == nil {
		status.LocalRecords = len(sessions)
	}
	if m.store != nil {
		if records, err := m.store.QueryUsageRange(ctx, time.Now().UTC().Add(-cursorBackfillWindow), time.Time{}); err == nil {
			for _, record := range records {
				if record.Source != "cursor" {
					continue
				}
				switch record.Provenance {
				case cursorProvenanceDashboard:
					status.CloudMatchedEvents++
				case cursorProvenanceLocalExact, cursorProvenanceLocalEstimated:
					status.LocalRecords++
				}
				tokens := record.InputTokens + record.OutputTokens + record.CacheReadTokens + record.CacheWriteTokens
				status.TotalTokens += tokens
				if record.TokenQuality == core.UsageTokenQualityEstimated {
					status.EstimatedTokens += tokens
				}
			}
		}
	}
	if !state.Connected {
		status.LocalStatus = map[bool]string{true: "available", false: "not_found"}[fileExists(m.dbPath) || cursorCLIAvailable(m.home)]
		status.CloudStatus = "not_connected"
	}
	return status
}

func (m *CursorUsageManager) Action(ctx context.Context, action string) (CursorUsageActionResult, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "preview":
		return CursorUsageActionResult{OK: true, Action: action, Message: "Cursor usage connection preview", Status: m.Status(ctx)}, nil
	case "connect":
		if _, err := installCursorHook(m.home); err != nil {
			return CursorUsageActionResult{}, err
		}
		auth, err := readCursorAuthForHome(ctx, m.home, m.dbPath)
		if err != nil {
			_, _ = removeCursorHook(m.home)
			return CursorUsageActionResult{}, err
		}
		if _, err := validateCursorDashboard(ctx, m.http, auth); err != nil {
			_, _ = removeCursorHook(m.home)
			return CursorUsageActionResult{}, err
		}
		m.mu.Lock()
		m.state.Connected = true
		m.state.BackfillFrom = time.Now().UTC().Add(-cursorBackfillWindow).Format(time.RFC3339Nano)
		m.state.BackfillPage = 1
		m.state.BackfillComplete = false
		m.state.LastError = ""
		m.state.LocalStatus = "pending"
		m.state.CloudStatus = "pending"
		state := m.state
		m.mu.Unlock()
		if err := m.saveState(ctx, state); err != nil {
			return CursorUsageActionResult{}, err
		}
		m.triggerSync(true)
		return CursorUsageActionResult{OK: true, Action: action, Started: true, Message: "Cursor usage connection enabled", Status: m.Status(ctx)}, nil
	case "sync":
		if !m.connected() {
			return CursorUsageActionResult{}, errCursorNotConnected
		}
		m.triggerSync(true)
		return CursorUsageActionResult{OK: true, Action: action, Started: true, Message: "Cursor usage sync started", Status: m.Status(ctx)}, nil
	case "repair":
		if !m.connected() {
			return CursorUsageActionResult{}, errCursorNotConnected
		}
		if _, err := installCursorHook(m.home); err != nil {
			return CursorUsageActionResult{}, err
		}
		auth, err := readCursorAuthForHome(ctx, m.home, m.dbPath)
		if err != nil {
			return CursorUsageActionResult{}, err
		}
		if _, err := validateCursorDashboard(ctx, m.http, auth); err != nil {
			return CursorUsageActionResult{}, err
		}
		m.triggerSync(true)
		return CursorUsageActionResult{OK: true, Action: action, Started: true, Message: "Cursor usage connection repaired", Status: m.Status(ctx)}, nil
	case "disconnect":
		if _, err := removeCursorHook(m.home); err != nil {
			return CursorUsageActionResult{}, err
		}
		m.mu.Lock()
		m.state.Connected = false
		m.state.CloudStatus = "not_connected"
		m.state.LocalStatus = "not_connected"
		m.state.LastError = ""
		state := m.state
		m.mu.Unlock()
		if err := m.saveState(ctx, state); err != nil {
			return CursorUsageActionResult{}, err
		}
		return CursorUsageActionResult{OK: true, Action: action, Message: "Cursor usage connection disabled; collected history was preserved", Status: m.Status(ctx)}, nil
	default:
		return CursorUsageActionResult{}, fmt.Errorf("unsupported Cursor usage action %q", action)
	}
}

func (m *CursorUsageManager) triggerSync(includeCloud bool) {
	if m == nil || !m.connected() {
		return
	}
	m.mu.Lock()
	ctx := m.runCtx
	if m.syncing {
		// A local scan and the hourly cloud tick are phase-aligned because one
		// hour is exactly divisible by the 30-second local interval. Never drop
		// the higher-value cloud request when the local scan wins that race.
		m.pendingCloud = m.pendingCloud || includeCloud
		m.mu.Unlock()
		return
	}
	m.syncing = true
	m.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	go m.runSyncLoop(ctx, includeCloud)
}

func (m *CursorUsageManager) runSyncLoop(ctx context.Context, includeCloud bool) {
	for {
		syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		err := m.executeSync(syncCtx, includeCloud)
		timedOut := syncCtx.Err() != nil
		cancel()
		if err != nil && !timedOut {
			m.log.Warn("Cursor usage sync failed", "err", err)
		}

		m.mu.Lock()
		if m.pendingCloud && m.state.Connected && ctx.Err() == nil {
			m.pendingCloud = false
			includeCloud = true
			m.mu.Unlock()
			continue
		}
		m.pendingCloud = false
		m.syncing = false
		m.mu.Unlock()
		return
	}
}

func (m *CursorUsageManager) executeSync(ctx context.Context, includeCloud bool) error {
	if m.syncFn != nil {
		return m.syncFn(ctx, includeCloud)
	}
	return m.sync(ctx, includeCloud)
}

func (m *CursorUsageManager) sync(ctx context.Context, includeCloud bool) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if !m.connected() {
		return errCursorNotConnected
	}
	m.mu.Lock()
	state := m.state
	m.mu.Unlock()
	localErr := m.syncLocal(ctx, &state)
	var cloudErr error
	if includeCloud {
		cloudErr = m.syncCloud(ctx, &state)
	}
	if localErr != nil || cloudErr != nil {
		state.LastError = safeCursorSyncError(errors.Join(localErr, cloudErr))
	} else if includeCloud {
		state.LastError = ""
		state.LastSyncAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	m.mu.Lock()
	stateToSave := m.state
	if m.state.Connected {
		state.Connected = true
		m.state = state
		stateToSave = state
	}
	m.mu.Unlock()
	if err := m.saveState(ctx, stateToSave); err != nil {
		return errors.Join(localErr, cloudErr, err)
	}
	return errors.Join(localErr, cloudErr)
}

func (m *CursorUsageManager) syncLocal(ctx context.Context, state *cursorUsageState) error {
	state.LocalStatus = "scanning"
	since := time.Now().UTC().Add(-cursorBackfillWindow)
	desktopAvailable := fileExists(m.dbPath)
	if desktopAvailable {
		complete := false
		for batches := 0; batches < 1000; batches++ {
			batch, err := collectCursorLocalBatch(ctx, m.dbPath, state.LocalRowID, since)
			if err != nil {
				state.LocalStatus = "error"
				return err
			}
			if len(batch.Records) > 0 {
				if err := m.recordBatch(ctx, batch.Records); err != nil {
					state.LocalStatus = "error"
					return err
				}
				state.LocalRecords += len(batch.Records)
			}
			state.LocalRowID = batch.LastRowID
			if !batch.More {
				complete = true
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if !complete {
			state.LocalStatus = "error"
			return errors.New("Cursor local scan exceeded the safety batch limit")
		}
	}
	cliSessions, err := discoverCursorCLISessions(ctx, m.home, since)
	if err != nil {
		state.LocalStatus = "error"
		return err
	}
	if !desktopAvailable && !cursorCLIAvailable(m.home) {
		state.LocalStatus = "error"
		return errors.New("Cursor local data was not found")
	}
	if !desktopAvailable {
		state.LocalRecords = len(cliSessions)
	}
	state.LocalStatus = "ready"
	return nil
}

func (m *CursorUsageManager) syncCloud(ctx context.Context, state *cursorUsageState) error {
	state.CloudStatus = "syncing"
	auth, err := readCursorAuthForHome(ctx, m.home, m.dbPath)
	if err != nil {
		state.CloudStatus = "error"
		return err
	}
	auth, err = validateCursorDashboard(ctx, m.http, auth)
	if err != nil {
		state.CloudStatus = "error"
		return err
	}
	now := time.Now().UTC()
	since := cursorCloudSyncStart(now, *state)
	page := 1
	if !state.BackfillComplete {
		if state.BackfillPage > 0 {
			page = state.BackfillPage
		}
	}
	local, err := m.store.QueryUsageRequestIndex(ctx, "cursor", now.Add(-cursorBackfillWindow))
	if err != nil {
		state.CloudStatus = "error"
		return err
	}
	for _, record := range local {
		if record.Provenance == cursorProvenanceDashboard {
			continue
		}
		if sessionID := firstCursorValue(record.ConversationID, record.SessionID); sessionID != "" {
			local[cursorConversationIndexKey(sessionID)] = record
		}
	}
	cliSessions, err := discoverCursorCLISessions(ctx, m.home, now.Add(-cursorBackfillWindow))
	if err != nil {
		state.CloudStatus = "error"
		return err
	}
	for key, record := range cliSessions {
		local[key] = record
	}
	// A headless CLI login is account-wide, while the fleet report is
	// machine-scoped. Only accept cloud events whose conversation exists in
	// this machine's CLI inventory, otherwise connecting the same account on
	// two hosts would double count every event.
	restrictToLocal := !fileExists(m.dbPath) && cursorCLIAvailable(m.home)
	result, err := fetchCursorDashboardScoped(ctx, m.http, auth, since, now, page, local, restrictToLocal)
	if err != nil {
		state.CloudStatus = "error"
		return err
	}
	if len(result.Records) > 0 {
		if err := m.recordBatch(ctx, result.Records); err != nil {
			state.CloudStatus = "error"
			return err
		}
	}
	state.CloudMatchedEvents = result.Matched
	state.CloudIgnoredEvents = result.Ignored
	if !state.BackfillComplete {
		state.BackfillPage = result.NextPage
		state.BackfillComplete = result.Complete
	}
	state.CloudStatus = map[bool]string{true: "ready", false: "partial"}[result.Complete || state.BackfillComplete]
	return nil
}

func cursorCloudSyncStart(now time.Time, state cursorUsageState) time.Time {
	now = now.UTC()
	floor := now.Add(-cursorBackfillWindow)
	if !state.BackfillComplete {
		if parsed, err := time.Parse(time.RFC3339Nano, state.BackfillFrom); err == nil {
			if parsed.Before(floor) {
				return floor
			}
			if parsed.Before(now) {
				return parsed
			}
		}
		return floor
	}
	if lastSync, err := time.Parse(time.RFC3339Nano, state.LastSyncAt); err == nil && lastSync.Before(now) {
		since := lastSync.Add(-cursorCloudOverlap)
		if since.Before(floor) {
			return floor
		}
		return since
	}
	return now.Add(-cursorCloudOverlap)
}

func (m *CursorUsageManager) connected() bool {
	m.mu.Lock()
	connected := m.state.Connected
	m.mu.Unlock()
	return connected
}

func (m *CursorUsageManager) loadState(ctx context.Context) {
	if m.store == nil {
		return
	}
	raw, ok, err := m.store.GetSetting(ctx, cursorUsageStateKey)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return
	}
	var state cursorUsageState
	if json.Unmarshal([]byte(raw), &state) == nil {
		m.state = state
	}
}

func (m *CursorUsageManager) saveState(ctx context.Context, state cursorUsageState) error {
	if m.store == nil {
		return nil
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return m.store.SetSetting(ctx, cursorUsageStateKey, string(raw))
}

func safeCursorSyncError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if len(message) > 300 {
		message = message[:300]
	}
	return message
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
