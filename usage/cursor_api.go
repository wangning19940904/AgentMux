package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const (
	cursorDashboardOrigin     = "https://cursor.com"
	cursorDashboardEventsURL  = cursorDashboardOrigin + "/api/dashboard/get-filtered-usage-events"
	cursorDashboardSummaryURL = cursorDashboardOrigin + "/api/usage-summary"
	cursorOAuthTokenURL       = "https://api2.cursor.sh/oauth/token"
	cursorOAuthClientID       = "KbZUR41cY7W6zRSdpSUJ7I7mLYBKOCmB"
	cursorDashboardPageSize   = 100
	cursorDashboardMaxPages   = 100
	cursorMaxResponseBytes    = 16 << 20
)

type cursorAuth struct {
	AccessToken  string
	RefreshToken string
	MachineID    string
	SessionToken string
}

type cursorDashboardResult struct {
	Records     []core.UsageRecord
	Matched     int
	Ignored     int
	Fetched     int
	Complete    bool
	NextPage    int
	TotalEvents int
}

type cursorDashboardResponse struct {
	TotalUsageEventsCount int                    `json:"totalUsageEventsCount"`
	UsageEvents           []cursorDashboardEvent `json:"usageEvents"`
	UsageEventsDisplay    []cursorDashboardEvent `json:"usageEventsDisplay"`
	Pagination            struct {
		NumPages    int  `json:"numPages"`
		CurrentPage int  `json:"currentPage"`
		HasNextPage bool `json:"hasNextPage"`
	} `json:"pagination"`
}

type cursorDashboardEvent struct {
	ID             string          `json:"id"`
	RequestID      string          `json:"requestId"`
	UsageEventID   string          `json:"usageEventId"`
	GenerationID   string          `json:"generationId"`
	GenerationUUID string          `json:"generationUUID"`
	ConversationID string          `json:"conversationId"`
	CloudAgentID   string          `json:"cloudAgentId"`
	Timestamp      json.RawMessage `json:"timestamp"`
	CreatedAt      json.RawMessage `json:"createdAt"`
	Model          string          `json:"model"`
	ModelName      string          `json:"modelName"`
	Kind           string          `json:"kind"`
	IsFreeBugbot   bool            `json:"isFreeBugbot"`
	InputTokens    float64         `json:"inputTokens"`
	OutputTokens   float64         `json:"outputTokens"`
	TotalCents     *float64        `json:"totalCents"`
	ChargedCents   *float64        `json:"chargedCents"`
	TokenUsage     *struct {
		InputTokens      float64  `json:"inputTokens"`
		OutputTokens     float64  `json:"outputTokens"`
		CacheWriteTokens float64  `json:"cacheWriteTokens"`
		CacheReadTokens  float64  `json:"cacheReadTokens"`
		TotalCents       *float64 `json:"totalCents"`
	} `json:"tokenUsage"`
}

type cursorHTTPError struct {
	Status int
	Body   string
}

func (e *cursorHTTPError) Error() string {
	return fmt.Sprintf("cursor usage API returned HTTP %d%s", e.Status, map[bool]string{true: ": " + e.Body}[e.Body != ""])
}

func newCursorHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func readCursorAuth(ctx context.Context, dbPath string) (cursorAuth, error) {
	db, err := openCursorReadOnly(dbPath)
	if err != nil {
		return cursorAuth{}, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key,value FROM ItemTable WHERE key IN (
		'cursorAuth/accessToken','cursorAuth/refreshToken','storage.serviceMachineId')`)
	if err != nil {
		return cursorAuth{}, err
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return cursorAuth{}, err
		}
		values[key] = strings.TrimSpace(value)
	}
	if err := rows.Err(); err != nil {
		return cursorAuth{}, err
	}
	auth := cursorAuth{
		AccessToken: values["cursorAuth/accessToken"], RefreshToken: values["cursorAuth/refreshToken"],
		MachineID: values["storage.serviceMachineId"],
	}
	if auth.AccessToken == "" {
		return cursorAuth{}, errors.New("Cursor is not signed in on this machine")
	}
	auth.SessionToken, err = cursorSessionToken(auth.AccessToken)
	if err != nil {
		return cursorAuth{}, err
	}
	return auth, nil
}

func cursorSessionToken(accessToken string) (string, error) {
	parts := strings.Split(strings.TrimSpace(accessToken), ".")
	if len(parts) < 2 {
		return "", errors.New("Cursor access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errors.New("Cursor access token has an invalid JWT payload")
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil || strings.TrimSpace(claims.Subject) == "" {
		return "", errors.New("Cursor access token is missing its subject")
	}
	userID := claims.Subject
	if index := strings.LastIndex(userID, "|"); index >= 0 {
		userID = userID[index+1:]
	}
	return userID + "::" + strings.TrimSpace(accessToken), nil
}

func validateCursorDashboard(ctx context.Context, client *http.Client, auth cursorAuth) (cursorAuth, error) {
	err := cursorSummaryRequest(ctx, client, auth)
	var httpErr *cursorHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized || auth.RefreshToken == "" {
		return auth, err
	}
	refreshed, refreshErr := refreshCursorAuth(ctx, client, auth)
	if refreshErr != nil {
		return auth, refreshErr
	}
	return refreshed, cursorSummaryRequest(ctx, client, refreshed)
}

func cursorSummaryRequest(ctx context.Context, client *http.Client, auth cursorAuth) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cursorDashboardSummaryURL, nil)
	if err != nil {
		return err
	}
	applyCursorHeaders(req, auth, false)
	var ignored map[string]any
	return cursorDoJSON(client, req, &ignored)
}

func refreshCursorAuth(ctx context.Context, client *http.Client, auth cursorAuth) (cursorAuth, error) {
	body, _ := json.Marshal(map[string]string{
		"grant_type": "refresh_token", "client_id": cursorOAuthClientID, "refresh_token": auth.RefreshToken,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorOAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return auth, err
	}
	req.Header.Set("Content-Type", "application/json")
	var response struct {
		AccessToken  string `json:"access_token"`
		ShouldLogout bool   `json:"shouldLogout"`
		Message      string `json:"message"`
	}
	if err := cursorDoJSON(client, req, &response); err != nil {
		return auth, err
	}
	if response.ShouldLogout || strings.TrimSpace(response.AccessToken) == "" {
		return auth, errors.New("Cursor rejected the local refresh token; sign in to Cursor again")
	}
	auth.AccessToken = strings.TrimSpace(response.AccessToken)
	auth.SessionToken, err = cursorSessionToken(auth.AccessToken)
	return auth, err
}

func fetchCursorDashboard(ctx context.Context, client *http.Client, auth cursorAuth, since, until time.Time, startPage int, local map[string]core.UsageRecord) (cursorDashboardResult, error) {
	return fetchCursorDashboardScoped(ctx, client, auth, since, until, startPage, local, false)
}

func fetchCursorDashboardScoped(ctx context.Context, client *http.Client, auth cursorAuth, since, until time.Time, startPage int, local map[string]core.UsageRecord, restrictToLocal bool) (cursorDashboardResult, error) {
	if startPage <= 0 {
		startPage = 1
	}
	result := cursorDashboardResult{Complete: false, NextPage: startPage}
	for page := startPage; page < startPage+cursorDashboardMaxPages; page++ {
		response, nextAuth, err := fetchCursorDashboardPage(ctx, client, auth, since, until, page)
		if err != nil {
			return result, err
		}
		auth = nextAuth
		events := response.UsageEvents
		if len(events) == 0 {
			events = response.UsageEventsDisplay
		}
		result.TotalEvents = response.TotalUsageEventsCount
		result.Fetched += len(events)
		for _, event := range events {
			record, matched, ok := cursorRecordFromDashboardEventScoped(event, local, restrictToLocal)
			if !ok {
				result.Ignored++
				continue
			}
			if matched {
				result.Matched++
			}
			result.Records = append(result.Records, record)
		}
		hasNext := response.Pagination.HasNextPage
		if !hasNext && response.Pagination.NumPages > 0 {
			hasNext = page < response.Pagination.NumPages
		}
		if !hasNext && (len(events) == 0 || len(events) < cursorDashboardPageSize || response.TotalUsageEventsCount <= page*cursorDashboardPageSize) {
			result.Complete = true
			result.NextPage = 0
			return result, nil
		}
		result.NextPage = page + 1
	}
	return result, nil
}

func fetchCursorDashboardPage(ctx context.Context, client *http.Client, auth cursorAuth, since, until time.Time, page int) (cursorDashboardResponse, cursorAuth, error) {
	payload := map[string]any{
		"startDate": since.UnixMilli(), "endDate": until.UnixMilli(), "page": page, "pageSize": cursorDashboardPageSize,
	}
	body, _ := json.Marshal(payload)
	do := func(current cursorAuth) (cursorDashboardResponse, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorDashboardEventsURL, bytes.NewReader(body))
		if err != nil {
			return cursorDashboardResponse{}, err
		}
		applyCursorHeaders(req, current, true)
		var response cursorDashboardResponse
		err = cursorDoJSON(client, req, &response)
		return response, err
	}
	response, err := do(auth)
	var httpErr *cursorHTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized && auth.RefreshToken != "" {
		refreshed, refreshErr := refreshCursorAuth(ctx, client, auth)
		if refreshErr != nil {
			return cursorDashboardResponse{}, auth, refreshErr
		}
		auth = refreshed
		response, err = do(auth)
	}
	return response, auth, err
}

func applyCursorHeaders(req *http.Request, auth cursorAuth, origin bool) {
	req.Header.Set("Cookie", "WorkosCursorSessionToken="+auth.SessionToken)
	req.Header.Set("Accept", "application/json")
	if req.Method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin {
		req.Header.Set("Origin", cursorDashboardOrigin)
	}
	if auth.MachineID != "" {
		req.Header.Set("x-cursor-client-id", auth.MachineID)
	}
}

func cursorDoJSON(client *http.Client, req *http.Request, target any) error {
	if req.URL.Scheme != "https" || (req.URL.Host != "cursor.com" && req.URL.Host != "api2.cursor.sh") {
		return fmt.Errorf("refusing unexpected Cursor API host %q", req.URL.Host)
	}
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, cursorMaxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(raw) > cursorMaxResponseBytes {
		return errors.New("Cursor usage API response exceeded the size limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &cursorHTTPError{Status: response.StatusCode, Body: cursorSafeAPIError(raw)}
	}
	if len(raw) == 0 {
		return errors.New("Cursor usage API returned an empty response")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return errors.New("Cursor usage API returned invalid JSON")
	}
	return nil
}

func cursorSafeAPIError(raw []byte) string {
	var parsed struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &parsed) == nil {
		return firstCursorValue(parsed.Error, parsed.Message)
	}
	return "request failed"
}

func cursorRecordFromDashboardEventScoped(event cursorDashboardEvent, local map[string]core.UsageRecord, restrictToLocal bool) (core.UsageRecord, bool, bool) {
	if event.IsFreeBugbot || strings.Contains(strings.ToLower(event.Kind), "bugbot") {
		return core.UsageRecord{}, false, false
	}
	requestID := firstCursorValue(event.ID, event.RequestID, event.UsageEventID, event.GenerationID, event.GenerationUUID)
	sessionID := firstCursorValue(event.ConversationID, event.CloudAgentID)
	var matchedRecord core.UsageRecord
	matched := false
	if requestID != "" {
		matchedRecord, matched = local[requestID]
	}
	if !matched && sessionID != "" {
		matchedRecord, matched = local[cursorConversationIndexKey(sessionID)]
	}
	if restrictToLocal && !matched {
		return core.UsageRecord{}, false, false
	}
	if matched {
		sessionID = firstCursorValue(matchedRecord.SessionID, sessionID)
	}
	if !matched && sessionID == "" {
		return core.UsageRecord{}, false, false
	}
	timestamp := cursorTimestampFromRaw(event.Timestamp)
	if timestamp.IsZero() {
		timestamp = cursorTimestampFromRaw(event.CreatedAt)
	}
	if timestamp.IsZero() {
		return core.UsageRecord{}, matched, false
	}
	model := firstCursorValue(event.Model, event.ModelName, matchedRecord.Model, "cursor")
	input, output := event.InputTokens, event.OutputTokens
	cacheRead, cacheWrite := float64(0), float64(0)
	var totalCents *float64
	if event.TokenUsage != nil {
		if input == 0 {
			input = event.TokenUsage.InputTokens
		}
		if output == 0 {
			output = event.TokenUsage.OutputTokens
		}
		cacheRead, cacheWrite = event.TokenUsage.CacheReadTokens, event.TokenUsage.CacheWriteTokens
		totalCents = event.TokenUsage.TotalCents
	}
	if event.TotalCents != nil {
		totalCents = event.TotalCents
	}
	if event.ChargedCents != nil {
		totalCents = event.ChargedCents
	}
	if requestID == "" {
		requestID = cursorDashboardSyntheticID(timestamp, model, input, output, cacheRead, cacheWrite)
	}
	record := core.UsageRecord{
		Source: "cursor", RuntimeID: "cursor", SessionID: sessionID, ConversationID: sessionID,
		RequestID: requestID, Project: matchedRecord.Project, Model: model, Timestamp: timestamp,
		InputTokens: clampCursorToken(input), OutputTokens: clampCursorToken(output),
		CacheReadTokens: clampCursorToken(cacheRead), CacheWriteTokens: clampCursorToken(cacheWrite),
		Provenance: cursorProvenanceDashboard, ProvenanceRank: cursorRankDashboard,
		TokenQuality: core.UsageTokenQualityExact, CostKind: core.UsageCostKindCalculated,
	}
	if record.InputTokens+record.OutputTokens+record.CacheReadTokens+record.CacheWriteTokens == 0 {
		record.TokenQuality = core.UsageTokenQualityUnknown
	}
	if totalCents != nil {
		record.CostUSD = maxFloat(0, *totalCents) / 100
		record.CostKind = core.UsageCostKindRecorded
	}
	return record, matched, true
}

func cursorTimestampFromRaw(raw json.RawMessage) time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return time.Time{}
	}
	var number float64
	if json.Unmarshal(raw, &number) == nil {
		return cursorTimestamp(number)
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return parsed.UTC()
		}
		if number, err := strconv.ParseFloat(text, 64); err == nil {
			return cursorTimestamp(number)
		}
	}
	return time.Time{}
}

func clampCursorToken(value float64) int64 {
	if value <= 0 {
		return 0
	}
	return int64(value + 0.5)
}

func cursorDashboardSyntheticID(timestamp time.Time, model string, tokens ...float64) string {
	h := sha256.New()
	_, _ = io.WriteString(h, timestamp.UTC().Format(time.RFC3339Nano))
	_, _ = io.WriteString(h, "\x00"+model)
	for _, token := range tokens {
		_, _ = io.WriteString(h, "\x00"+strconv.FormatInt(clampCursorToken(token), 10))
	}
	return "cursor-" + hex.EncodeToString(h.Sum(nil))[:24]
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
