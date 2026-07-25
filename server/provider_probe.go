package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

const providerProbeTimeout = 15 * time.Second

var providerProbeAPIFormats = []string{"anthropic", "openai_chat", "openai_responses"}
var providerProbeProtocols = []string{"chat_completions", "responses"}

type providerProbeCheck struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	OK      bool     `json:"ok"`
	URL     string   `json:"url,omitempty"`
	Status  int      `json:"status,omitempty"`
	Models  []string `json:"models,omitempty"`
	Message string   `json:"message,omitempty"`
}

type providerProbeResult struct {
	OK           bool                 `json:"ok"`
	URL          string               `json:"url,omitempty"`
	Models       []string             `json:"models"`
	Count        int                  `json:"count"`
	Message      string               `json:"message"`
	APIFormat    string               `json:"api_format,omitempty"`
	CodexWireAPI string               `json:"codex_wire_api,omitempty"`
	Formats      []providerProbeCheck `json:"formats,omitempty"`
	Protocols    []providerProbeCheck `json:"protocols,omitempty"`
}

func (s *Server) handleProviderProbe(w http.ResponseWriter, r *http.Request) {
	var p core.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := normalizeProviderAPIKey(&p); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, err := probeProviderModels(r.Context(), &p)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func probeProviderModels(ctx context.Context, p *core.Provider) (providerProbeResult, error) {
	baseURL := strings.TrimSpace(p.BaseURL)
	if baseURL == "" {
		return providerProbeResult{}, fmt.Errorf("missing request URL")
	}
	apiKeyEnv := strings.TrimSpace(p.APIKeyEnv)
	if apiKeyEnv == "" {
		return providerProbeResult{}, fmt.Errorf("missing API key env")
	}
	apiKey := os.Getenv(apiKeyEnv)
	if apiKey == "" {
		return providerProbeResult{}, fmt.Errorf("environment variable %s is empty or not set", apiKeyEnv)
	}

	client := &http.Client{Timeout: providerProbeTimeout}
	var lastErr error
	var formats []providerProbeCheck
	modelSet := map[string]bool{}
	firstURL := ""
	for _, apiFormat := range orderedProbeAPIFormats(p.Meta.APIFormat) {
		check := providerProbeCheck{Kind: "api_format", Name: apiFormat}
		for _, endpoint := range candidateModelURLs(baseURL, apiFormat) {
			models, status, body, err := fetchProviderModels(ctx, client, endpoint, apiKey, apiFormat)
			check.URL = endpoint
			check.Status = status
			if err != nil {
				check.Message = err.Error()
				lastErr = err
				continue
			}
			if status < 200 || status >= 300 {
				check.Message = fmt.Sprintf("HTTP %d: %s", status, body)
				lastErr = fmt.Errorf("%s returned HTTP %d: %s", endpoint, status, body)
				continue
			}
			check.OK = true
			check.Models = models
			check.Message = "OK"
			if firstURL == "" {
				firstURL = endpoint
			}
			for _, model := range models {
				modelSet[model] = true
			}
			break
		}
		if check.URL == "" && check.Message == "" {
			check.Message = "no model endpoint candidates"
		}
		formats = append(formats, check)
	}

	models := make([]string, 0, len(modelSet))
	for model := range modelSet {
		models = append(models, model)
	}
	sort.Strings(models)
	apiFormat := recommendAPIFormat(formats, p.Meta.APIFormat)
	if apiFormat == "" {
		if lastErr == nil {
			lastErr = fmt.Errorf("no model endpoint candidates for %s", baseURL)
		}
		return providerProbeResult{Formats: formats}, lastErr
	}

	probeModel := strings.TrimSpace(p.Model)
	if probeModel == "" && len(models) > 0 {
		probeModel = models[0]
	}
	protocolBaseURL := baseURL
	if firstURL != "" {
		protocolBaseURL = firstURL
	}
	protocols := probeProviderProtocols(ctx, client, protocolBaseURL, apiKey, probeModel)
	message := "Connection OK."
	if len(models) == 0 {
		message = "Connection OK, but no models were found."
	}
	return providerProbeResult{
		OK:           true,
		URL:          firstURL,
		Models:       models,
		Count:        len(models),
		Message:      message,
		APIFormat:    apiFormat,
		CodexWireAPI: recommendCodexWireAPI(protocols),
		Formats:      formats,
		Protocols:    protocols,
	}, nil
}

func orderedProbeAPIFormats(preferred string) []string {
	out := make([]string, 0, len(providerProbeAPIFormats))
	seen := map[string]bool{}
	if preferred != "" {
		out = append(out, preferred)
		seen[preferred] = true
	}
	for _, apiFormat := range providerProbeAPIFormats {
		if !seen[apiFormat] {
			out = append(out, apiFormat)
			seen[apiFormat] = true
		}
	}
	return out
}

func recommendAPIFormat(formats []providerProbeCheck, preferred string) string {
	if preferred != "" {
		for _, check := range formats {
			if check.Name == preferred && check.OK {
				return preferred
			}
		}
	}
	for _, check := range formats {
		if check.OK {
			return check.Name
		}
	}
	return ""
}

func recommendCodexWireAPI(protocols []providerProbeCheck) string {
	for _, check := range protocols {
		if check.Name == "responses" && check.OK {
			return "responses"
		}
	}
	for _, check := range protocols {
		if check.Name == "chat_completions" && check.OK {
			return "chat"
		}
	}
	return ""
}

func probeProviderProtocols(ctx context.Context, client *http.Client, baseURL, apiKey, model string) []providerProbeCheck {
	out := make([]providerProbeCheck, 0, len(providerProbeProtocols))
	for _, protocol := range providerProbeProtocols {
		check := providerProbeCheck{Kind: "protocol", Name: protocol}
		if model == "" {
			check.Message = "no model available"
			out = append(out, check)
			continue
		}
		endpoint, err := providerProtocolURL(baseURL, protocol)
		if err != nil {
			check.Message = err.Error()
			out = append(out, check)
			continue
		}
		status, body, err := postProviderProtocol(ctx, client, endpoint, apiKey, model, protocol)
		check.URL = endpoint
		check.Status = status
		if err != nil {
			check.Message = err.Error()
			out = append(out, check)
			continue
		}
		if status < 200 || status >= 300 {
			check.Message = fmt.Sprintf("HTTP %d: %s", status, body)
			out = append(out, check)
			continue
		}
		check.OK = true
		check.Message = "OK"
		out = append(out, check)
	}
	return out
}

func providerProtocolURL(baseURL, protocol string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid request URL")
	}
	trimmedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.HasSuffix(trimmedPath, "/models") {
		trimmedPath = strings.TrimSuffix(trimmedPath, "/models")
	}
	if !strings.HasSuffix(trimmedPath, "/v1") {
		trimmedPath += "/v1"
	}
	switch protocol {
	case "chat_completions":
		parsed.Path = trimmedPath + "/chat/completions"
	case "responses":
		parsed.Path = trimmedPath + "/responses"
	default:
		return "", fmt.Errorf("unsupported protocol %q", protocol)
	}
	return parsed.String(), nil
}

func postProviderProtocol(ctx context.Context, client *http.Client, endpoint, apiKey, model, protocol string) (int, string, error) {
	var payload map[string]any
	switch protocol {
	case "chat_completions":
		payload = map[string]any{
			"model":      model,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
			"stream":     false,
		}
	case "responses":
		payload = map[string]any{
			"model":             model,
			"input":             "ping",
			"max_output_tokens": 1,
			"stream":            false,
		}
	default:
		return 0, "", fmt.Errorf("unsupported protocol %q", protocol)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	res, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return res.StatusCode, "", err
	}
	return res.StatusCode, truncateBody(body), nil
}

func candidateModelURLs(baseURL, apiFormat string) []string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	trimmedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	if strings.HasSuffix(trimmedPath, "/models") {
		parsed.Path = trimmedPath
		return []string{parsed.String()}
	}

	var paths []string
	switch apiFormat {
	case "anthropic":
		if strings.HasSuffix(trimmedPath, "/v1") {
			paths = append(paths, trimmedPath+"/models")
		} else {
			paths = append(paths, trimmedPath+"/v1/models")
			paths = append(paths, trimmedPath+"/models")
		}
	default:
		paths = append(paths, trimmedPath+"/models")
		if !strings.HasSuffix(trimmedPath, "/v1") {
			paths = append(paths, trimmedPath+"/v1/models")
		}
	}

	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		u := *parsed
		u.Path = path
		value := u.String()
		if !seen[value] {
			out = append(out, value)
			seen[value] = true
		}
	}
	return out
}

func fetchProviderModels(ctx context.Context, client *http.Client, endpoint, apiKey, apiFormat string) ([]string, int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, "", err
	}
	req.Header.Set("Accept", "application/json")
	if apiFormat == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, 0, "", err
	}
	defer res.Body.Close()

	data, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, res.StatusCode, "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, res.StatusCode, truncateBody(data), nil
	}
	models, err := extractModelIDs(data)
	if err != nil {
		return nil, res.StatusCode, truncateBody(data), err
	}
	return models, res.StatusCode, truncateBody(data), nil
}

func extractModelIDs(data []byte) ([]string, error) {
	var payload any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var models []string
	var walk func(any)
	walk = func(value any) {
		switch item := value.(type) {
		case []any:
			for _, entry := range item {
				walk(entry)
			}
		case map[string]any:
			if id, ok := item["id"].(string); ok {
				id = strings.TrimSpace(id)
				if id != "" && !seen[id] {
					models = append(models, id)
					seen[id] = true
				}
			}
			for _, key := range []string{"data", "models"} {
				if nested, ok := item[key]; ok {
					walk(nested)
				}
			}
		case string:
			id := strings.TrimSpace(item)
			if id != "" && !seen[id] {
				models = append(models, id)
				seen[id] = true
			}
		}
	}
	walk(payload)
	sort.Strings(models)
	return models, nil
}

func truncateBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	if len(body) > 600 {
		return body[:600] + "..."
	}
	return body
}
