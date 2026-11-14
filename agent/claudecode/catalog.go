package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/agent/internal/runner"
	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/internal/procutil"
)

const (
	catalogSuccessTTL = 10 * time.Minute
	catalogFailureTTL = time.Minute
	catalogTimeout    = 20 * time.Second
	catalogRequestID  = "agentmux-catalog-1"
)

// claudeModelInfo is the subset of the Agent SDK's ModelInfo that the CLI
// returns in its stream-json `initialize` control response.
type claudeModelInfo struct {
	Value                 string   `json:"value"`
	DisplayName           string   `json:"displayName"`
	SupportsEffort        bool     `json:"supportsEffort"`
	SupportedEffortLevels []string `json:"supportedEffortLevels"`
}

type claudeCatalog struct {
	models []claudeModelInfo
}

type catalogCacheEntry struct {
	catalog   claudeCatalog
	err       error
	refreshAt time.Time
}

var (
	catalogCacheMu sync.Mutex
	catalogCache   = map[string]catalogCacheEntry{}
)

// RuntimeSettingsCatalog reports the models the installed Claude Code offers
// with its current configuration. It asks the CLI itself, so whatever
// configures Claude Code (an official login, a gateway or a router that
// rewrites settings.json) is reflected without AgentMux knowing about it. No
// prompt is sent and no conversation is created.
func (a *Agent) RuntimeSettingsCatalog(ctx context.Context, workDir string) (core.RuntimeSettings, core.RuntimeSettingsCapabilities, error) {
	catalog, err := a.discoverCatalog(ctx, workDir)
	if err != nil {
		return core.RuntimeSettings{}, core.RuntimeSettingsCapabilities{}, err
	}
	defaults := core.RuntimeSettings{
		Model:           a.defaultModel,
		ReasoningEffort: a.defaultReasoningEffort,
		ApprovalMode:    a.defaultApprovalMode,
	}
	return defaults, catalog.capabilities(a.supportedApprovalModes), nil
}

func (c claudeCatalog) capabilities(approvalModes []string) core.RuntimeSettingsCapabilities {
	models := make([]core.RuntimeOption, 0, len(c.models))
	perModel := make(map[string]core.RuntimeModelCapabilities, len(c.models))
	var allEfforts []string
	for _, model := range c.models {
		models = append(models, core.RuntimeOption{Value: model.Value, Label: model.DisplayName})
		var efforts []string
		if model.SupportsEffort || len(model.SupportedEffortLevels) > 0 {
			efforts = model.SupportedEffortLevels
		}
		allEfforts = append(allEfforts, efforts...)
		perModel[model.Value] = core.RuntimeModelCapabilities{
			ReasoningEfforts: core.RuntimeOptionsFor(core.RuntimeSettingReasoningEffort, efforts),
		}
	}
	return core.RuntimeSettingsCapabilities{
		Models:            models,
		ReasoningEfforts:  core.RuntimeOptionsFor(core.RuntimeSettingReasoningEffort, allEfforts),
		ApprovalModes:     core.RuntimeOptionsFor(core.RuntimeSettingApprovalMode, approvalModes),
		ModelCapabilities: perModel,
	}
}

func (a *Agent) catalogCacheKey(workDir string) string {
	keys := make([]string, 0, len(a.env))
	for key, value := range a.env {
		keys = append(keys, key+"="+value)
	}
	sort.Strings(keys)
	return claudeBinary() + "\x00" + workDir + "\x00" + strings.Join(keys, "\x00")
}

func (a *Agent) discoverCatalog(ctx context.Context, workDir string) (claudeCatalog, error) {
	if workDir == "" {
		workDir, _ = os.Getwd()
	}
	key := a.catalogCacheKey(workDir)
	catalogCacheMu.Lock()
	defer catalogCacheMu.Unlock()
	if entry, ok := catalogCache[key]; ok && time.Now().Before(entry.refreshAt) {
		return entry.catalog, entry.err
	}
	catalog, err := a.readInitializeModels(ctx, workDir)
	ttl := catalogSuccessTTL
	if err != nil {
		ttl = catalogFailureTTL
		err = fmt.Errorf("discover claude models: %w", err)
	}
	catalogCache[key] = catalogCacheEntry{catalog: catalog, err: err, refreshAt: time.Now().Add(ttl)}
	return catalog, err
}

// readInitializeModels starts Claude Code in stream-json input mode, sends the
// `initialize` control request (the same one the Agent SDK sends for
// supportedModels()) and stops the process once the response arrives.
func (a *Agent) readInitializeModels(ctx context.Context, workDir string) (claudeCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, catalogTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, claudeBinary(),
		"--print", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence")
	procutil.Prepare(cmd)
	cmd.Dir = workDir
	cmd.Env = runner.BuildEnv(a.env, "CLAUDECODE")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return claudeCatalog{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return claudeCatalog{}, err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return claudeCatalog{}, err
	}
	defer func() {
		_ = stdin.Close()
		cancel()
		_ = cmd.Wait()
	}()
	request, _ := json.Marshal(map[string]any{
		"type": "control_request", "request_id": catalogRequestID,
		"request": map[string]any{"subtype": "initialize"},
	})
	if _, err := stdin.Write(append(request, '\n')); err != nil {
		return claudeCatalog{}, err
	}
	catalog, err := scanInitializeResponse(stdout)
	if err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return claudeCatalog{}, fmt.Errorf("%w: %s", err, detail)
		}
		if ctx.Err() != nil {
			return claudeCatalog{}, fmt.Errorf("%w: %v", err, ctx.Err())
		}
	}
	return catalog, err
}

func scanInitializeResponse(r io.Reader) (claudeCatalog, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					Models []claudeModelInfo `json:"models"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "control_response" ||
			line.Response.RequestID != catalogRequestID {
			continue
		}
		if line.Response.Subtype != "success" {
			if line.Response.Error == "" {
				line.Response.Error = "initialize failed"
			}
			return claudeCatalog{}, errors.New(line.Response.Error)
		}
		var models []claudeModelInfo
		for _, model := range line.Response.Response.Models {
			model.Value = strings.TrimSpace(model.Value)
			if model.Value != "" {
				models = append(models, model)
			}
		}
		if len(models) == 0 {
			return claudeCatalog{}, errors.New("initialize response listed no models")
		}
		return claudeCatalog{models: models}, nil
	}
	if err := sc.Err(); err != nil {
		return claudeCatalog{}, err
	}
	return claudeCatalog{}, errors.New("claude exited before answering initialize")
}

// limitedWriter keeps the first n bytes so a chatty CLI cannot grow memory.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		chunk := p
		if len(chunk) > l.n {
			chunk = chunk[:l.n]
		}
		written, _ := l.w.Write(chunk)
		l.n -= written
	}
	return len(p), nil
}
