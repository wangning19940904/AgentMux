package usage

import (
	"sort"
	"strings"

	"github.com/wangning19940904/AgentMux/core"
)

// ReportFilter narrows a report to the listed values of each dimension. An
// empty list leaves that dimension unrestricted; values within one dimension
// are alternatives, and dimensions combine.
type ReportFilter struct {
	Models   []string // model names as recorded; "" is the unknown model
	Runtimes []string // framework IDs as UsageRuntime reports them
	Agents   []string // agent/project keys as usageAgentKey reports them
	Sources  []string // collector IDs
}

// Empty reports whether the filter restricts nothing.
func (f ReportFilter) Empty() bool {
	return len(f.Models) == 0 && len(f.Runtimes) == 0 && len(f.Agents) == 0 && len(f.Sources) == 0
}

func (f ReportFilter) apply(records []core.UsageRecord) []core.UsageRecord {
	if f.Empty() {
		return records
	}
	models, runtimes, agents, sources := valueSet(f.Models), valueSet(f.Runtimes), valueSet(f.Agents), valueSet(f.Sources)
	kept := records[:0]
	for _, record := range records {
		if models != nil && !models[record.Model] {
			continue
		}
		if runtimes != nil && !runtimes[record.UsageRuntime()] {
			continue
		}
		if agents != nil && !agents[usageAgentKey(record)] {
			continue
		}
		if sources != nil && !sources[record.Source] {
			continue
		}
		kept = append(kept, record)
	}
	return kept
}

func valueSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

// UnknownAgent is the agent key of a record that names no project or agent.
const UnknownAgent = "unknown"

// TemporaryAgent is the agent key of records made in a temporary directory.
const TemporaryAgent = "(temporary directory)"

var temporaryDirectories = []string{"/private/var/folders/", "/var/folders/", "/private/tmp/", "/tmp/"}

// usageAgentKey is the agent or project a record belongs to. Records made
// through AgentMux carry "agent:<id>" or "channel:<id>"; transcript records
// carry the working directory. Work trees fold into one key per repository,
// since every task otherwise shows up as a project of its own.
func usageAgentKey(rec core.UsageRecord) string {
	project := strings.TrimSpace(rec.Project)
	if project == "" {
		return UnknownAgent
	}
	// Claude Code names a project by its directory with separators replaced:
	// "<repo>--claude-worktrees-<name>".
	if index := strings.Index(project, "--claude-worktrees-"); index > 0 {
		return project[:index]
	}
	if index := strings.Index(project, "/.claude/worktrees/"); index > 0 {
		return project[:index]
	}
	// One-off runs start in a fresh temporary directory each time. Thousands
	// of them are one kind of work, not thousands of projects.
	for _, prefix := range temporaryDirectories {
		if strings.HasPrefix(project, prefix) {
			return TemporaryAgent
		}
	}
	// Codex work trees live in ".codex/worktrees/<id>/<repo>".
	const codexWorktrees = "/.codex/worktrees/"
	if index := strings.Index(project, codexWorktrees); index >= 0 {
		rest := project[index+len(codexWorktrees):]
		if _, repo, ok := strings.Cut(rest, "/"); ok && repo != "" {
			return project[:index] + codexWorktrees + "*/" + repo
		}
	}
	return project
}

// Facets list the values of each filterable dimension found in a time range,
// largest first, with the tokens recorded for each.
type Facets struct {
	Models   []FacetValue `json:"models"`
	Runtimes []FacetValue `json:"runtimes"`
	Agents   []FacetValue `json:"agents"`
}

// FacetValue is one choice of a filterable dimension.
type FacetValue struct {
	Value  string `json:"value"`
	Tokens int64  `json:"tokens"`
}

// maxFacetValues bounds each list: a long-lived machine sees hundreds of
// project directories, and the smallest are not useful filter choices.
const maxFacetValues = 200

func usageFacets(records []core.UsageRecord) *Facets {
	models, runtimes, agents := map[string]int64{}, map[string]int64{}, map[string]int64{}
	for _, record := range records {
		tokens := totalTokens(record)
		models[record.Model] += tokens
		runtimes[record.UsageRuntime()] += tokens
		agents[usageAgentKey(record)] += tokens
	}
	return &Facets{Models: facetValues(models), Runtimes: facetValues(runtimes), Agents: facetValues(agents)}
}

func facetValues(totals map[string]int64) []FacetValue {
	values := make([]FacetValue, 0, len(totals))
	for value, tokens := range totals {
		values = append(values, FacetValue{Value: value, Tokens: tokens})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].Tokens != values[j].Tokens {
			return values[i].Tokens > values[j].Tokens
		}
		return values[i].Value < values[j].Value
	})
	if len(values) > maxFacetValues {
		values = values[:maxFacetValues]
	}
	return values
}
