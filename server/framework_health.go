package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/wangning19940904/AgentMux/framework"
)

const (
	frameworkHealthSetting    = "framework_health_v1"
	frameworkHealthMaxEntries = 200
)

// frameworkHealthMu serialises read-modify-write of the stored results.
var frameworkHealthMu sync.Mutex

// recordFrameworkHealth keeps the latest smoke test result per framework and
// model on this machine. It is display state only: nothing blocks on it.
func (s *Server) recordFrameworkHealth(ctx context.Context, result framework.SmokeResult) error {
	if s.st == nil {
		return nil
	}
	frameworkHealthMu.Lock()
	defer frameworkHealthMu.Unlock()
	results, err := s.loadFrameworkHealth(ctx)
	if err != nil {
		return err
	}
	kept := results[:0]
	for _, existing := range results {
		if existing.Kind != result.Kind || existing.Model != result.Model {
			kept = append(kept, existing)
		}
	}
	kept = append(kept, result)
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].CheckedAt.After(kept[j].CheckedAt) })
	if len(kept) > frameworkHealthMaxEntries {
		kept = kept[:frameworkHealthMaxEntries]
	}
	raw, err := json.Marshal(kept)
	if err != nil {
		return err
	}
	return s.st.SetSetting(ctx, frameworkHealthSetting, string(raw))
}

func (s *Server) loadFrameworkHealth(ctx context.Context) ([]framework.SmokeResult, error) {
	if s.st == nil {
		return nil, nil
	}
	raw, ok, err := s.st.GetSetting(ctx, frameworkHealthSetting)
	if err != nil || !ok {
		return nil, err
	}
	var results []framework.SmokeResult
	if json.Unmarshal([]byte(raw), &results) != nil {
		return nil, nil // unreadable display state is simply forgotten
	}
	return results, nil
}

// handleFrameworkHealth lists the latest smoke test results, newest first,
// optionally narrowed to one framework.
func (s *Server) handleFrameworkHealth(w http.ResponseWriter, r *http.Request) {
	results, err := s.loadFrameworkHealth(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		filtered := results[:0]
		for _, result := range results {
			if result.Kind == kind {
				filtered = append(filtered, result)
			}
		}
		results = filtered
	}
	if results == nil {
		results = []framework.SmokeResult{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
