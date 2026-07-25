// Package guard implements AgentMux Guard: the permission-approval and
// policy gate for tool calls. The default "policy" guard evaluates a tool
// call against ordered rules stored in the SQLite SSOT and falls back to a
// configurable default decision (ask) when no rule matches.
package guard

import (
	"context"

	"github.com/wangning19940904/AgentMux/core"
	"github.com/wangning19940904/AgentMux/store"
)

func init() {
	core.RegisterGuard("policy", func(cfg map[string]any) (core.Guard, error) {
		def := core.GuardAsk
		if d, ok := cfg["default"].(string); ok && d != "" {
			def = core.GuardDecision(d)
		}
		return &PolicyGuard{def: def}, nil
	})
}

// PolicyGuard implements core.Guard against stored policy rules.
type PolicyGuard struct {
	st  *store.Store
	def core.GuardDecision
}

var _ core.Guard = (*PolicyGuard)(nil)

// New builds a store-backed policy guard with the given default decision.
func New(st *store.Store, def core.GuardDecision) *PolicyGuard {
	if def == "" {
		def = core.GuardAsk
	}
	return &PolicyGuard{st: st, def: def}
}

// Name returns the guard id.
func (g *PolicyGuard) Name() string { return "policy" }

// Evaluate returns the policy decision for a tool-call request. Rules are
// matched by tool (and optional action) in descending priority order; the
// first match wins. With no store wired or no match, the default applies.
func (g *PolicyGuard) Evaluate(ctx context.Context, req *core.GuardRequest) (core.GuardDecision, error) {
	if g.st == nil {
		return g.def, nil
	}
	policies, err := g.st.ListGuardPolicies(ctx)
	if err != nil {
		return g.def, err
	}
	for _, p := range policies {
		if !matches(p.Tool, req.Tool) {
			continue
		}
		if p.Action != "" && !matches(p.Action, req.Action) {
			continue
		}
		return core.GuardDecision(p.Decision), nil
	}
	return g.def, nil
}

// matches supports exact match and a "*" wildcard.
func matches(pattern, val string) bool {
	return pattern == "*" || pattern == val
}
