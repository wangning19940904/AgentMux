package server

import (
	"net/http"

	"github.com/wangning19940904/AgentMux/usage"
)

// scopeUsageReport narrows a machine-wide usage report to one tenant.
//
// Only the per-agent breakdown can be attributed to an owner, so a tenant sees
// its own agents' rows with totals recomputed from them. The dimensions that
// aggregate across the whole host - period buckets, per-model, per-source and
// per-runtime - are dropped rather than reported unfiltered, because they
// would otherwise disclose a peer's spend.
func (s *Server) scopeUsageReport(r *http.Request, report any) (any, error) {
	principal := requestPrincipal(r)
	if !principal.IsTenant() {
		return report, nil
	}
	typed, ok := report.(*usage.Report)
	if !ok {
		// An unrecognised reporter shape cannot be filtered safely, so it is
		// withheld instead of leaked.
		return map[string]any{}, nil
	}

	visible, err := s.visibleAgentIDs(r.Context(), principal)
	if err != nil {
		return nil, err
	}

	scoped := &usage.Report{
		Period:    typed.Period,
		From:      typed.From,
		To:        typed.To,
		Timezone:  typed.Timezone,
		Buckets:   []usage.Bucket{},
		ByModel:   []usage.ModelStat{},
		BySource:  []usage.SourceStat{},
		ByAgent:   []usage.AgentStat{},
		ByRuntime: []usage.RuntimeStat{},
	}
	for _, row := range typed.ByAgent {
		if !visible[row.Agent] {
			continue
		}
		scoped.ByAgent = append(scoped.ByAgent, row)
		scoped.Totals.CostUSD += row.CostUSD
		scoped.Totals.Records += row.Records
		// The per-agent rows carry a single token total rather than the input
		// and output split, so it is reported as input tokens to keep the
		// grand total honest instead of silently zero.
		scoped.Totals.InputTokens += row.Tokens
	}
	return scoped, nil
}
