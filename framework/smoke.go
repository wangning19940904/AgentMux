package framework

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// SmokePrompt is the only input a smoke test sends.
const SmokePrompt = "Reply with exactly: ok"

const (
	DefaultSmokeTimeout = 60 * time.Second
	MaxSmokeTimeout     = 5 * time.Minute
	smokeMaxConcurrent  = 2
	smokeReplyLimit     = 200
	smokeDetailLimit    = 600
)

// SmokeErrorKind classifies why a smoke test failed. The classification is
// based only on what the framework reports, so it works the same whether the
// framework talks to its vendor directly or through a gateway or router.
type SmokeErrorKind string

const (
	SmokeNotInstalled    SmokeErrorKind = "not_installed"
	SmokeUnauthenticated SmokeErrorKind = "unauthenticated"
	SmokeModelNotFound   SmokeErrorKind = "model_not_found"
	SmokeRateLimited     SmokeErrorKind = "rate_limited"
	SmokeNetwork         SmokeErrorKind = "network"
	SmokeTimeout         SmokeErrorKind = "timeout"
	SmokeUnexpectedReply SmokeErrorKind = "unexpected_reply"
	SmokeUnknown         SmokeErrorKind = "unknown"
)

// SmokeOptions selects what to test. Model, ReasoningEffort and ServiceTier
// are passed to the framework exactly as an Agent would pass them; an empty
// Model tests the framework's own default.
type SmokeOptions struct {
	Kind            string
	Model           string
	ReasoningEffort string
	ServiceTier     string
	Env             map[string]string
	Timeout         time.Duration
}

// SmokeResult is the outcome of one smoke test.
type SmokeResult struct {
	Kind          string         `json:"kind"`
	Model         string         `json:"model,omitempty"`
	ResolvedModel string         `json:"resolved_model,omitempty"`
	OK            bool           `json:"ok"`
	LatencyMs     int64          `json:"latency_ms"`
	Reply         string         `json:"reply,omitempty"`
	ErrorKind     SmokeErrorKind `json:"error_kind,omitempty"`
	ErrorDetail   string         `json:"error_detail,omitempty"`
	CheckedAt     time.Time      `json:"checked_at"`
}

// ErrUnknownFramework is returned when no agent runtime is registered for the
// requested kind.
var ErrUnknownFramework = errors.New("framework runtime is not registered")

var smokeSlots = make(chan struct{}, smokeMaxConcurrent)

// SmokeTest checks that a framework works end to end with a model: it starts
// the framework through the same agent runtime real Agents use, sends
// SmokePrompt in an empty temporary directory, and checks the reply. It costs
// one short model call, so callers run it only on demand.
//
// The test does not inspect how the framework reaches its model. Providers,
// keys and routing stay entirely in the framework's own configuration.
func SmokeTest(ctx context.Context, opts SmokeOptions) (SmokeResult, error) {
	kind := strings.TrimSpace(opts.Kind)
	result := SmokeResult{Kind: kind, Model: strings.TrimSpace(opts.Model), CheckedAt: time.Now().UTC()}
	if kind == "" || !core.HasAgent(kind) {
		return result, ErrUnknownFramework
	}
	if _, catalogued := Lookup(kind); catalogued && !IsInstalled(kind) {
		result.ErrorKind = SmokeNotInstalled
		result.ErrorDetail = "the framework CLI was not found on this machine"
		return result, nil
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultSmokeTimeout
	}
	timeout = min(timeout, MaxSmokeTimeout)

	select {
	case smokeSlots <- struct{}{}:
		defer func() { <-smokeSlots }()
	case <-ctx.Done():
		return result, ctx.Err()
	}

	dir, err := os.MkdirTemp("", "agentmux-health-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	run := runSmoke(runCtx, kind, dir, opts, result.Model)
	result.LatencyMs = time.Since(started).Milliseconds()
	result.ResolvedModel = run.resolvedModel
	result.Reply = truncateRunes(strings.TrimSpace(run.reply), smokeReplyLimit)

	if smokeReplyOK(run.reply) {
		result.OK = true
		return result, nil
	}
	if ctx.Err() != nil {
		// The caller went away; this says nothing about the framework.
		return result, ctx.Err()
	}
	evidence := strings.Join(append(run.errs, run.reply), "\n")
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		result.ErrorKind = SmokeTimeout
		if strings.TrimSpace(evidence) == "" {
			evidence = fmt.Sprintf("no reply within %s", timeout)
		}
	case len(run.errs) == 0 && strings.TrimSpace(run.reply) != "":
		result.ErrorKind = classifySmokeError(run.reply)
		if result.ErrorKind == SmokeUnknown {
			result.ErrorKind = SmokeUnexpectedReply
		}
	default:
		result.ErrorKind = classifySmokeError(evidence)
		if strings.TrimSpace(evidence) == "" {
			evidence = "the framework ended without a reply"
		}
	}
	result.ErrorDetail = truncateRunes(redactSecrets(strings.TrimSpace(evidence)), smokeDetailLimit)
	return result, nil
}

type smokeRun struct {
	reply         string
	resolvedModel string
	errs          []string
}

func runSmoke(ctx context.Context, kind, dir string, opts SmokeOptions, model string) smokeRun {
	var run smokeRun
	cfg := map[string]any{
		"work_dir":                  dir,
		"single_agent":              true,
		core.AgentConfigHealthCheck: true,
	}
	if model != "" {
		cfg["model"] = model
	}
	if v := strings.TrimSpace(opts.ReasoningEffort); v != "" {
		cfg["reasoning_effort"] = v
	}
	if v := strings.TrimSpace(opts.ServiceTier); v != "" {
		cfg["service_tier"] = v
	}
	if modes := core.ApprovalModeValuesForRuntime(kind); slices.Contains(modes, core.ApprovalModePlan) {
		cfg["approval_mode"] = core.ApprovalModePlan
	}
	if len(opts.Env) > 0 {
		cfg["env"] = opts.Env
	}

	agent, err := core.CreateAgent(kind, cfg)
	if err != nil {
		run.errs = append(run.errs, err.Error())
		return run
	}
	defer func() { _ = agent.Stop(context.WithoutCancel(ctx)) }()
	session, err := agent.StartSession(ctx, dir)
	if err != nil {
		run.errs = append(run.errs, err.Error())
		return run
	}
	defer func() { _ = session.Close(context.WithoutCancel(ctx)) }()
	events, err := session.Send(ctx, SmokePrompt)
	if err != nil {
		run.errs = append(run.errs, err.Error())
		return run
	}

	var streamed strings.Builder
	final := ""
	for {
		select {
		case <-ctx.Done():
			run.reply = firstNonEmpty(final, streamed.String())
			return run
		case ev, ok := <-events:
			if !ok {
				run.reply = firstNonEmpty(final, streamed.String())
				return run
			}
			if ev == nil {
				continue
			}
			if ev.Usage != nil && run.resolvedModel == "" {
				run.resolvedModel = firstNonEmpty(ev.Usage.ResolvedModel, ev.Usage.Model)
			}
			switch ev.Type {
			case core.EventOutput:
				streamed.WriteString(ev.Text)
			case core.EventFinal:
				if strings.TrimSpace(ev.Text) != "" {
					final = ev.Text
				}
			case core.EventError:
				if ev.Err != nil {
					run.errs = append(run.errs, ev.Err.Error())
				} else if ev.Text != "" {
					run.errs = append(run.errs, ev.Text)
				}
			case core.EventPermission:
				// The prompt needs no tools; refuse anything the model asks for.
				declineSmokePermission(ctx, session, ev)
			}
		}
	}
}

func declineSmokePermission(ctx context.Context, session core.AgentSession, ev *core.Event) {
	if interactive, ok := session.(core.InteractiveAgentSession); ok && ev.Interaction != nil {
		_ = interactive.ResolveInteraction(ctx, ev.Interaction.ID, core.AgentInteractionResponse{
			Decision: "decline", Answers: map[string][]string{},
		})
		return
	}
	_ = session.RespondPermission(ctx, false)
}

var smokeOKWord = regexp.MustCompile(`(?i)\bok\b`)

// smokeReplyOK accepts "ok" with surrounding punctuation, quotes or markdown
// and short replies that contain the word, such as "OK." or "ok!".
func smokeReplyOK(reply string) bool {
	reply = strings.TrimSpace(reply)
	if reply == "" || len([]rune(reply)) > 40 {
		return false
	}
	return smokeOKWord.MatchString(reply)
}

type smokePattern struct {
	kind SmokeErrorKind
	re   *regexp.Regexp
}

// Order matters: the first match wins.
var smokePatterns = []smokePattern{
	{SmokeUnauthenticated, regexp.MustCompile(`(?i)\b(401|403)\b|unauthori[sz]ed|forbidden|invalid[ _-]?(x-)?api[ _-]?key|authentication|not (logged|signed) in|log ?in required|please run /login|/login|oauth token|expired token|credential`)},
	{SmokeModelNotFound, regexp.MustCompile(`(?i)model[ _]not[ _]found|unknown model|invalid model|no such model|unsupported model|model .{0,80}(does not exist|not (be )?(found|available|supported))|not_found_error|\b404\b`)},
	{SmokeRateLimited, regexp.MustCompile(`(?i)\b(429|529)\b|rate[ _-]?limit|too many requests|quota|usage limit|overloaded|insufficient[ _]balance|credit balance`)},
	{SmokeNetwork, regexp.MustCompile(`(?i)connection refused|econnrefused|econnreset|enotfound|no such host|dial tcp|connection reset|unable to connect|could not connect|fetch failed|network|tls handshake|certificate|proxy`)},
	{SmokeTimeout, regexp.MustCompile(`(?i)timed? ?out|deadline exceeded|etimedout`)},
}

func classifySmokeError(text string) SmokeErrorKind {
	for _, pattern := range smokePatterns {
		if pattern.re.MatchString(text) {
			return pattern.kind
		}
	}
	return SmokeUnknown
}

var smokeSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bsk-[A-Za-z0-9_\-]{8,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/\-]{8,}=*`),
	regexp.MustCompile(`(?i)\b((x-)?api[_-]?key|token|secret|password)(["'\s]*[:=]\s*["']?)[^\s"',}]{6,}`),
}

func redactSecrets(text string) string {
	for i, re := range smokeSecretPatterns {
		if i == 2 {
			text = re.ReplaceAllString(text, "$1$3[redacted]")
			continue
		}
		text = re.ReplaceAllString(text, "[redacted]")
	}
	return text
}

func truncateRunes(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
