package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

type InsightEngine struct {
	store *store.Store
	mu    sync.Mutex
}

func NewInsightEngine(st *store.Store) *InsightEngine { return &InsightEngine{store: st} }

type insightToolStats struct {
	agentID    string
	name       string
	total      int64
	failed     int64
	outputs    []int64
	traces     []string
	retryLoops int64
}

type insightAgentStats struct {
	agentID           string
	total             int64
	requests          int64
	failed            int64
	requestFailures   int64
	input             int64
	cacheRead         int64
	cacheWrite        int64
	compactions       int64
	maxTokens         int64
	reroutes          int64
	traces            []string
	compactionTrace   []string
	schemaTokens      int64
	contextTokens     int64
	schemaSamples     int64
	recentDurations   []int64
	baselineDurations []int64
	recentTTFT        []int64
	baselineTTFT      []int64
	latencyTraces     []string
}

type insightHookStats struct {
	total     int64
	failed    int64
	durations []int64
	traces    []string
}

type insightModelStats struct {
	agentID         string
	model           string
	total           int64
	cost            float64
	traces          []string
	comparisonGroup string
}

type insightSpanSelection struct {
	traceID string
	span    store.ObservationSpan
}

// Run evaluates advisory-only rules against up to the latest 1000 traces.
// It is safe to call repeatedly; insight IDs are stable per rule/group.
func (e *InsightEngine) Run(ctx context.Context, since time.Time) ([]store.ObservationInsight, error) {
	if e == nil || e.store == nil {
		return nil, nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if since.IsZero() {
		since = time.Now().UTC().Add(-7 * 24 * time.Hour)
	}
	traces, err := e.store.ListObservationTraces(ctx, store.ObservationTraceFilter{Since: since, Limit: 1000})
	if err != nil {
		return nil, err
	}
	tools := map[string]*insightToolStats{}
	agents := map[string]*insightAgentStats{}
	hooks := map[string]*insightHookStats{}
	models := map[string]*insightModelStats{}
	recentCutoff := time.Now().UTC().Add(-24 * time.Hour)
	traceIDs := make([]string, 0, len(traces))
	for _, trace := range traces {
		traceIDs = append(traceIDs, trace.TraceID)
	}
	spansByTrace, err := e.store.ListObservationSpansForTraces(ctx, traceIDs)
	if err != nil {
		return nil, err
	}
	selectedModels := map[string]insightSpanSelection{}
	selectedTools := map[string]insightSpanSelection{}
	for _, trace := range traces {
		spans := spansByTrace[trace.TraceID]
		for _, span := range spans {
			var key string
			var selections map[string]insightSpanSelection
			switch span.Kind {
			case "model.request":
				key = insightModelRequestKey(trace, span)
				selections = selectedModels
			case "tool.call":
				key = insightToolCallKey(trace, span)
				selections = selectedTools
			default:
				continue
			}
			candidate := insightSpanSelection{traceID: trace.TraceID, span: span}
			if current, ok := selections[key]; !ok || insightPreferSpan(candidate, current) {
				selections[key] = candidate
			}
		}
	}
	for _, trace := range traces {
		agentID := firstNonEmptyValue(trace.AgentID, trace.AgentName, "unknown")
		agent := agents[agentID]
		if agent == nil {
			agent = &insightAgentStats{agentID: agentID}
			agents[agentID] = agent
		}
		agent.total++
		agent.traces = appendTrace(agent.traces, trace.TraceID)
		if trace.Status == core.ObservationStatusError {
			agent.failed++
		}
		spans := spansByTrace[trace.TraceID]
		failedToolsThisTrace := map[string]int64{}
		for _, span := range spans {
			switch span.Kind {
			case "model.request":
				if selected := selectedModels[insightModelRequestKey(trace, span)]; !insightSameSpan(selected, trace.TraceID, span.SpanID) {
					continue
				}
			case "tool.call":
				if selected := selectedTools[insightToolCallKey(trace, span)]; !insightSameSpan(selected, trace.TraceID, span.SpanID) {
					continue
				}
			}
			switch span.Kind {
			case "tool.call":
				name := "unknown"
				if span.Tool != nil && span.Tool.Name != "" {
					name = span.Tool.Name
				}
				key := agentID + "\x00" + name
				stat := tools[key]
				if stat == nil {
					stat = &insightToolStats{agentID: agentID, name: name}
					tools[key] = stat
				}
				// Count one completed span only, not its start/update rows.
				if span.EndedAt != nil || span.Status == core.ObservationStatusError || span.Status == core.ObservationStatusOK {
					stat.total++
					if span.Status == core.ObservationStatusError {
						stat.failed++
						failedToolsThisTrace[name]++
					}
					if span.Tool != nil && span.Tool.OutputBytes > 0 {
						stat.outputs = append(stat.outputs, span.Tool.OutputBytes/4)
					}
					stat.traces = appendTrace(stat.traces, trace.TraceID)
				}
			case "hook.run":
				key := span.Name
				stat := hooks[key]
				if stat == nil {
					stat = &insightHookStats{}
					hooks[key] = stat
				}
				stat.total++
				stat.durations = append(stat.durations, span.DurationMillis)
				if span.Status == core.ObservationStatusError {
					stat.failed++
				}
				stat.traces = appendTrace(stat.traces, trace.TraceID)
			case "compaction":
				agent.compactions++
				agent.compactionTrace = appendTrace(agent.compactionTrace, trace.TraceID)
			case "model.request":
				if span.Model != nil {
					terminal := span.EndedAt != nil || span.Status == core.ObservationStatusOK || span.Status == core.ObservationStatusError
					if terminal {
						agent.requests++
						agent.input += span.Usage.InputTokens
						agent.cacheRead += span.Usage.CacheReadTokens
						agent.cacheWrite += span.Usage.CacheWriteTokens
						finishReason := firstNonEmptyValue(span.Model.FinishReason, insightString(span.Attributes["finish_reason"]))
						if span.Status == core.ObservationStatusError || strings.EqualFold(finishReason, "refusal") {
							agent.requestFailures++
						}
						if span.StartedAt.After(recentCutoff) {
							agent.recentDurations = appendPositive(agent.recentDurations, span.DurationMillis)
							agent.recentTTFT = appendPositive(agent.recentTTFT, span.Model.TTFTMillis)
							agent.latencyTraces = appendTrace(agent.latencyTraces, trace.TraceID)
						} else {
							agent.baselineDurations = appendPositive(agent.baselineDurations, span.DurationMillis)
							agent.baselineTTFT = appendPositive(agent.baselineTTFT, span.Model.TTFTMillis)
						}
						if schemaTokens, contextTokens := insightInt(span.Attributes["tool_schema_tokens"]), insightInt(span.Attributes["context_tokens"]); schemaTokens > 0 && contextTokens > 0 {
							agent.schemaTokens += schemaTokens
							agent.contextTokens += contextTokens
							agent.schemaSamples++
						}
					}
					model := firstNonEmptyValue(span.Model.Resolved, span.Model.Requested)
					if model != "" && span.Status == core.ObservationStatusOK {
						effort := firstNonEmptyValue(span.Model.ReasoningEffort, insightString(span.Attributes["reasoning_effort"]), "default")
						group := agentID + "\x00" + effort + "\x00" + insightInputBucket(span.Usage.InputTokens)
						key := group + "\x00" + model
						stat := models[key]
						if stat == nil {
							stat = &insightModelStats{agentID: agentID, model: model, comparisonGroup: group}
							models[key] = stat
						}
						stat.total++
						stat.cost += span.Usage.CostUSD
						stat.traces = appendTrace(stat.traces, trace.TraceID)
					}
				}
			}
			if span.Attributes != nil {
				if value := fmt.Sprint(span.Attributes["finish_reason"]); value == "max_tokens" {
					agent.maxTokens++
				}
				if value := fmt.Sprint(span.Attributes["lifecycle"]); value == "rerouted" {
					agent.reroutes++
				}
			}
		}
		for name, failures := range failedToolsThisTrace {
			if failures >= 2 {
				if stat := tools[agentID+"\x00"+name]; stat != nil {
					stat.retryLoops++
				}
			}
		}
	}

	var insights []store.ObservationInsight
	for _, stat := range tools {
		if stat.retryLoops > 0 {
			insights = append(insights, newInsight("tool_retry_loop", stat.agentID+":"+stat.name,
				stat.agentID, "high", "Break repeated failure loop for "+stat.name,
				fmt.Sprintf("Detected repeated failures in %d traces.", stat.retryLoops),
				"Stop retrying unchanged inputs; classify the error and change parameters, permissions, or tool choice first.",
				stat.total, confidence(stat.total, 20), 0, 0, stat.traces))
		}
		if stat.total > 0 && float64(stat.failed)/float64(stat.total) >= 0.20 {
			insights = append(insights, newInsight("tool_failure_rate", stat.agentID+":"+stat.name,
				stat.agentID, "high", "Review failing tool "+stat.name,
				fmt.Sprintf("%d of %d calls failed (%.1f%%).", stat.failed, stat.total, percent(stat.failed, stat.total)),
				"Inspect the linked failures, tighten the tool contract, and remove blind retry loops before changing models.",
				stat.total, confidence(stat.total, 20), 0, 0, stat.traces))
		}
		if p95 := percentile95(stat.outputs); p95 > 8000 {
			savings := (p95 - 8000) * stat.total
			insights = append(insights, newInsight("tool_output_size", stat.agentID+":"+stat.name,
				stat.agentID, "medium", "Reduce tool output from "+stat.name,
				fmt.Sprintf("Estimated p95 tool output is %d tokens, above the 8k threshold.", p95),
				"Add pagination, field selection, or summarization so the model receives only decision-relevant output.",
				int64(len(stat.outputs)), confidence(int64(len(stat.outputs)), 20), savings, 0, stat.traces))
		}
	}
	for _, stat := range agents {
		if stat.requests >= 20 {
			denominator := stat.input + stat.cacheRead
			readRatio := ratio(stat.cacheRead, denominator)
			writeRatio := ratio(stat.cacheWrite, denominator+stat.cacheWrite)
			if readRatio < 0.20 || writeRatio > 0.30 {
				insights = append(insights, newInsight("cache_efficiency", stat.agentID, stat.agentID, "medium",
					"Review prompt-cache efficiency",
					fmt.Sprintf("Across %d requests, cache-read is %.1f%% and cache-write is %.1f%%.", stat.requests, readRatio*100, writeRatio*100),
					"Stabilize repeated prompt prefixes and move volatile context later; validate with an A/B trace comparison.",
					stat.requests, confidence(stat.requests, 20), int64(float64(stat.input)*0.20), 0, stat.traces))
			}
		}
		if stat.requests > 0 && ratio(stat.requestFailures, stat.requests) >= 0.05 {
			insights = append(insights, newInsight("agent_error_rate", stat.agentID, stat.agentID, "high",
				"Investigate elevated model errors", fmt.Sprintf("%d of %d recent requests failed or refused (%.1f%%).", stat.requestFailures, stat.requests, percent(stat.requestFailures, stat.requests)),
				"Cluster failures by model, tool and error code, then test the smallest targeted mitigation.", stat.requests,
				confidence(stat.requests, 20), 0, 0, stat.traces))
		}
		if stat.schemaSamples > 0 && ratio(stat.schemaTokens, stat.contextTokens) >= 0.02 {
			insights = append(insights, newInsight("unused_tool_schema", stat.agentID, stat.agentID, "medium",
				"Reduce unused tool schemas", fmt.Sprintf("Tool schemas occupy %.1f%% of measured context across %d requests.", percent(stat.schemaTokens, stat.contextTokens), stat.schemaSamples),
				"Load tools on demand or narrow the active tool set, then compare success rate before removing any capability.",
				stat.schemaSamples, confidence(stat.schemaSamples, 20), stat.schemaTokens, 0, stat.traces))
		}
		recentDuration, baselineDuration := percentile95(stat.recentDurations), percentile95(stat.baselineDurations)
		recentTTFT, baselineTTFT := percentile95(stat.recentTTFT), percentile95(stat.baselineTTFT)
		if (len(stat.recentDurations) >= 5 && len(stat.baselineDurations) >= 5 && baselineDuration > 0 && recentDuration >= 2*baselineDuration) ||
			(len(stat.recentTTFT) >= 5 && len(stat.baselineTTFT) >= 5 && baselineTTFT > 0 && recentTTFT >= 2*baselineTTFT) {
			insights = append(insights, newInsight("latency_regression", stat.agentID, stat.agentID, "high",
				"Investigate model latency regression", fmt.Sprintf("Recent p95 duration is %dms vs %dms baseline; TTFT is %dms vs %dms.", recentDuration, baselineDuration, recentTTFT, baselineTTFT),
				"Compare provider, model, input size, retries and network path using the linked traces before changing defaults.",
				int64(len(stat.recentDurations)), confidence(int64(len(stat.recentDurations)), 20), 0, 0, stat.latencyTraces))
		}
		if stat.compactions >= 2 && stat.total <= 10 || stat.maxTokens >= 2 || stat.reroutes >= 2 {
			insights = append(insights, newInsight("context_pressure", stat.agentID, stat.agentID, "medium",
				"Reduce context pressure and model mismatch",
				fmt.Sprintf("Observed %d compactions, %d max-token stops and %d reroutes.", stat.compactions, stat.maxTokens, stat.reroutes),
				"Trim repeated tool output, check requested/resolved model policy, and compare a smaller stable context window.",
				stat.total, confidence(stat.total, 10), 0, 0, append(stat.traces, stat.compactionTrace...)))
		}
	}
	for name, stat := range hooks {
		p95 := percentile95(stat.durations)
		if p95 > 200 || ratio(stat.failed, stat.total) > 0.01 {
			insights = append(insights, newInsight("hook_latency_error", name, "", "medium", "Optimize hook "+name,
				fmt.Sprintf("Hook p95 is %dms with %.1f%% errors across %d runs.", p95, percent(stat.failed, stat.total), stat.total),
				"Keep the hook as a bounded local enqueue and move network or analysis work behind the outbox.",
				stat.total, confidence(stat.total, 20), 0, 0, stat.traces))
		}
	}
	byAgent := map[string][]*insightModelStats{}
	for _, stat := range models {
		if stat.total >= 30 && stat.cost > 0 {
			byAgent[stat.comparisonGroup] = append(byAgent[stat.comparisonGroup], stat)
		}
	}
	for comparisonGroup, stats := range byAgent {
		if len(stats) < 2 {
			continue
		}
		sort.Slice(stats, func(i, j int) bool {
			return stats[i].cost/float64(stats[i].total) < stats[j].cost/float64(stats[j].total)
		})
		low, high := stats[0], stats[len(stats)-1]
		lowAverage, highAverage := low.cost/float64(low.total), high.cost/float64(high.total)
		if highAverage > 0 && (highAverage-lowAverage)/highAverage >= 0.30 {
			savings := (highAverage - lowAverage) * float64(high.total)
			insights = append(insights, newInsight("model_cost_ab", comparisonGroup+":"+low.model+":"+high.model, low.agentID, "low",
				"A/B test lower-cost model "+low.model,
				fmt.Sprintf("%s is %.1f%% cheaper per successful sample than %s.", low.model, (highAverage-lowAverage)/highAverage*100, high.model),
				"Run a controlled quality A/B test at the same effort and input-size band; do not auto-switch the model.",
				low.total+high.total, confidence(low.total+high.total, 60), 0, savings, append(low.traces, high.traces...)))
		}
	}
	for _, insight := range insights {
		if err := e.store.UpsertObservationInsight(ctx, insight); err != nil {
			return nil, err
		}
	}
	activeIDs := make([]string, 0, len(insights))
	for _, insight := range insights {
		activeIDs = append(activeIDs, insight.ID)
	}
	if err := e.store.ResolveObservationInsightsExcept(ctx, activeIDs); err != nil {
		return nil, err
	}
	return insights, nil
}

func newInsight(ruleID, group, agentID, severity, title, summary, suggestion string, sample int64, confidenceValue float64, tokenSavings int64, costSavings float64, traces []string) store.ObservationInsight {
	digest := sha256.Sum256([]byte(ruleID + "\x00" + group))
	return store.ObservationInsight{
		ID: "insight_" + hex.EncodeToString(digest[:12]), RuleID: ruleID, AgentID: agentID,
		Severity: severity, Status: "open", Title: title, Summary: summary, Suggestion: suggestion,
		SampleSize: sample, Confidence: confidenceValue, EstimatedTokenSavings: tokenSavings,
		EstimatedCostSavingsUSD: costSavings, RelatedTraceIDs: uniqueTraces(traces, 20), OnlySuggestion: true,
	}
}

func appendTrace(values []string, trace string) []string {
	if trace == "" {
		return values
	}
	for _, existing := range values {
		if existing == trace {
			return values
		}
	}
	if len(values) < 20 {
		values = append(values, trace)
	}
	return values
}

func uniqueTraces(values []string, limit int) []string {
	var out []string
	for _, value := range values {
		out = appendTrace(out, value)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func percentile95(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]int64(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i] < copyValues[j] })
	index := int(math.Ceil(float64(len(copyValues))*0.95)) - 1
	if index < 0 {
		index = 0
	}
	return copyValues[index]
}

func confidence(sample, target int64) float64 {
	if target <= 0 {
		target = 20
	}
	return math.Min(0.99, 0.5+0.49*float64(sample)/float64(target))
}

func ratio(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func percent(numerator, denominator int64) float64 { return ratio(numerator, denominator) * 100 }

func firstNonEmptyValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func appendPositive(values []int64, value int64) []int64 {
	if value > 0 {
		return append(values, value)
	}
	return values
}

func insightInt(value any) int64 {
	switch item := value.(type) {
	case int64:
		return item
	case int:
		return int64(item)
	case float64:
		return int64(item)
	case float32:
		return int64(item)
	case string:
		parsed, _ := strconv.ParseInt(item, 10, 64)
		return parsed
	default:
		return 0
	}
}

func insightString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func insightInputBucket(tokens int64) string {
	switch {
	case tokens <= 2_000:
		return "input<=2k"
	case tokens <= 8_000:
		return "input<=8k"
	case tokens <= 32_000:
		return "input<=32k"
	default:
		return "input>32k"
	}
}

// insightModelRequestKey identifies one model attempt across traces and
// collectors. Without both a runtime and a stable request ID we deliberately
// keep the span unique: guessing from turn/sequence can merge unrelated model
// calls in a multi-request turn.
func insightModelRequestKey(trace store.ObservationTrace, span store.ObservationSpan) string {
	runtimeID := insightRuntimeKey(firstNonEmptyValue(span.RuntimeID, trace.RuntimeID, span.Source))
	requestID := ""
	if span.Model != nil {
		requestID = span.Model.RequestID
	}
	requestID = firstNonEmptyValue(requestID,
		insightString(span.Attributes["request_id"]), insightString(span.Attributes["requestId"]),
		insightString(span.Attributes["response_id"]), insightString(span.Attributes["responseId"]))
	if runtimeID == "" || requestID == "" {
		return insightUniqueSpanKey("model", trace.TraceID, span.SpanID)
	}
	attempt := int64(0)
	if span.Model != nil {
		attempt = int64(span.Model.Attempt)
	}
	if attributeAttempt := insightInt(span.Attributes["attempt"]); attributeAttempt > 0 {
		attempt = attributeAttempt
	}
	if attempt <= 0 {
		attempt = 1
	}
	return "model\x00" + runtimeID + "\x00" + requestID + "\x00" + strconv.FormatInt(attempt, 10)
}

// insightToolCallKey identifies one tool invocation across hook, OTel and
// transcript representations. A missing session or call ID is not safe to
// deduplicate, so such spans retain their own identity.
func insightToolCallKey(trace store.ObservationTrace, span store.ObservationSpan) string {
	runtimeID := insightRuntimeKey(firstNonEmptyValue(span.RuntimeID, trace.RuntimeID, span.Source))
	sessionID := firstNonEmptyValue(span.SessionID, trace.SessionID)
	callID := ""
	if span.Tool != nil {
		callID = span.Tool.CallID
	}
	callID = firstNonEmptyValue(callID,
		insightString(span.Attributes["call_id"]), insightString(span.Attributes["callId"]),
		insightString(span.Attributes["tool_use_id"]), insightString(span.Attributes["toolUseId"]))
	if sessionID == "" || callID == "" {
		return insightUniqueSpanKey("tool", trace.TraceID, span.SpanID)
	}
	return "tool\x00" + runtimeID + "\x00" + sessionID + "\x00" + callID
}

func insightRuntimeKey(runtimeID string) string {
	runtimeID = strings.ToLower(strings.TrimSpace(runtimeID))
	switch {
	case strings.Contains(runtimeID, "codex"):
		return "codex"
	case strings.Contains(runtimeID, "claude"):
		return "claude"
	default:
		return runtimeID
	}
}

func insightUniqueSpanKey(kind, traceID, spanID string) string {
	return kind + "\x00unique\x00" + traceID + "\x00" + spanID
}

func insightSameSpan(selected insightSpanSelection, traceID, spanID string) bool {
	return selected.traceID == traceID && selected.span.SpanID == spanID
}

func insightPreferSpan(candidate, current insightSpanSelection) bool {
	if candidateRank, currentRank := insightSourceRank(candidate.span.Source), insightSourceRank(current.span.Source); candidateRank != currentRank {
		return candidateRank < currentRank
	}
	if candidateTerminal, currentTerminal := insightTerminalSpan(candidate.span), insightTerminalSpan(current.span); candidateTerminal != currentTerminal {
		return candidateTerminal
	}
	if candidateQuality, currentQuality := insightQualityRank(candidate.span.Quality), insightQualityRank(current.span.Quality); candidateQuality != currentQuality {
		return candidateQuality < currentQuality
	}
	if candidateRichness, currentRichness := insightSpanRichness(candidate.span), insightSpanRichness(current.span); candidateRichness != currentRichness {
		return candidateRichness > currentRichness
	}
	if !candidate.span.UpdatedAt.Equal(current.span.UpdatedAt) {
		return candidate.span.UpdatedAt.After(current.span.UpdatedAt)
	}
	return candidate.traceID+"\x00"+candidate.span.SpanID < current.traceID+"\x00"+current.span.SpanID
}

func insightTerminalSpan(span store.ObservationSpan) bool {
	return span.EndedAt != nil || span.Status == core.ObservationStatusOK || span.Status == core.ObservationStatusError || span.Status == core.ObservationStatusCancelled
}

func insightQualityRank(quality string) int {
	switch strings.ToLower(strings.TrimSpace(quality)) {
	case core.ObservationQualityComplete:
		return 0
	case core.ObservationQualityPartial:
		return 1
	case core.ObservationQualityInferred:
		return 2
	case core.ObservationQualityLegacy:
		return 3
	default:
		return 4
	}
}

func insightSpanRichness(span store.ObservationSpan) int64 {
	richness := span.Usage.TotalTokens + span.Usage.InputTokens + span.Usage.OutputTokens + span.Usage.CacheReadTokens + span.Usage.CacheWriteTokens
	if span.Model != nil {
		if span.Model.FinishReason != "" {
			richness++
		}
		if span.Model.TTFTMillis > 0 || span.Model.DurationMillis > 0 {
			richness++
		}
	}
	if span.Tool != nil {
		richness += span.Tool.InputBytes + span.Tool.OutputBytes
	}
	return richness
}

func insightSourceRank(source string) int {
	source = strings.ToLower(source)
	switch {
	case source == "agentmux.internal":
		return 0
	case strings.Contains(source, "otel") || strings.Contains(source, "app-server"):
		return 1
	case strings.Contains(source, "hook"):
		return 2
	case strings.Contains(source, "proxy"):
		return 3
	case strings.Contains(source, "transcript"):
		return 4
	default:
		return 5
	}
}
