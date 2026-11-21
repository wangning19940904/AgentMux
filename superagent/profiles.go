package superagent

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/wangning19940904/AgentMux/core"
)

// Worker profiles: the operator's model settings for auto workers of one
// framework. Each profile adds roster entries auto:<framework>/<profile>
// (and auto:<framework>/<profile>@<path>) next to the framework's plain
// ones, so the router matches the model and effort to how hard the work is.
// A profile is the operator's: a decision names one and cannot set a model
// or effort itself. Each profile has its own Agent instance wherever it runs,
// so a Ticket keeps its model across turns.

const (
	// profileSep separates a framework from its profile in a roster id.
	profileSep = "/"
	// profileAgentSep separates the profile in an Agent instance id. Path
	// ids may use '-', '_' and '.', so it is none of those.
	profileAgentSep = "+"

	maxWorkerProfiles = 16
	maxProfileModel   = 200
	maxProfileOption  = 40
	maxProfileUse     = 500
)

// profileNote explains profiles to the brain when the roster has any.
const profileNote = `Entries named auto:<framework>/<profile> launch that framework with one of the operator's model settings: "model", "effort" and "use" say which and what it is for. Match the profile to the work: the cheapest that can do it well for simple, well-defined work (translation, lookups, small edits, routine operations), a stronger one with more effort for hard, ambiguous or risky work (design, debugging, large changes). Entries without a profile use the framework's default model. When a finished ticket falls short because the work was harder than you thought, start the rest on a stronger profile with base_ticket set to it, instead of sending it back for rework.`

// validProfileID keeps profile ids usable in roster entries, Agent ids and
// directory names.
func validProfileID(id string) bool {
	if id == "" || len(id) > 32 {
		return false
	}
	for index, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || index > 0 && (r == '-' || r == '_')) {
			return false
		}
	}
	return true
}

// validProfileValue keeps a model, effort or tier name to one plain token.
func validProfileValue(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// checkProfileSettings checks the parts of a profile another machine is
// sent, on either side.
func checkProfileSettings(id, model, effort, tier string) error {
	if !validProfileID(id) {
		return fmt.Errorf("profile id %q must be 1 to 32 lowercase letters, digits, '-' or '_', starting with a letter or digit", id)
	}
	if !validProfileValue(model, maxProfileModel) {
		return fmt.Errorf("profile %q: model must be one name without spaces, at most %d characters", id, maxProfileModel)
	}
	if !validProfileValue(effort, maxProfileOption) || !validProfileValue(tier, maxProfileOption) {
		return fmt.Errorf("profile %q: reasoning effort and service tier must be one word each", id)
	}
	return nil
}

// normalizeWorkerProfiles checks the profiles. Each names one of the worker
// frameworks, is unique within it, and sets a model or an effort, since a
// profile with neither is the framework's plain entry.
func normalizeWorkerProfiles(agent *core.SuperAgent) error {
	if !agent.AutoWorkers {
		agent.WorkerProfiles = nil
		return nil
	}
	if len(agent.WorkerProfiles) > maxWorkerProfiles {
		return fmt.Errorf("an agent can have at most %d worker profiles", maxWorkerProfiles)
	}
	seen := map[string]bool{}
	for index := range agent.WorkerProfiles {
		profile := &agent.WorkerProfiles[index]
		profile.ID = strings.ToLower(strings.TrimSpace(profile.ID))
		profile.RuntimeID = strings.TrimSpace(profile.RuntimeID)
		profile.Model = strings.TrimSpace(profile.Model)
		profile.ReasoningEffort = strings.TrimSpace(profile.ReasoningEffort)
		profile.ServiceTier = strings.TrimSpace(profile.ServiceTier)
		profile.Use = strings.TrimSpace(profile.Use)
		profile.CostTier = strings.ToLower(strings.TrimSpace(profile.CostTier))
		if err := checkProfileSettings(profile.ID, profile.Model, profile.ReasoningEffort, profile.ServiceTier); err != nil {
			return err
		}
		if !slices.Contains(agent.WorkerRuntimes, profile.RuntimeID) {
			return fmt.Errorf("profile %q: %q is not one of this agent's worker frameworks", profile.ID, profile.RuntimeID)
		}
		key := profile.RuntimeID + profileSep + profile.ID
		if seen[key] {
			return fmt.Errorf("%s has two profiles named %q", profile.RuntimeID, profile.ID)
		}
		seen[key] = true
		if profile.Model == "" && profile.ReasoningEffort == "" {
			return fmt.Errorf("profile %q sets neither a model nor a reasoning effort", profile.ID)
		}
		if len(profile.Use) > maxProfileUse {
			return fmt.Errorf("profile %q: say what it is for in at most %d characters", profile.ID, maxProfileUse)
		}
		if err := checkTier(profile.CostTier); err != nil {
			return fmt.Errorf("profile %q: %w", profile.ID, err)
		}
	}
	return nil
}

// workerProfile finds a framework's profile.
func workerProfile(agent core.SuperAgent, runtimeID, id string) (core.SuperAgentWorkerProfile, bool) {
	index := slices.IndexFunc(agent.WorkerProfiles, func(profile core.SuperAgentWorkerProfile) bool {
		return profile.RuntimeID == runtimeID && profile.ID == id
	})
	if index < 0 {
		return core.SuperAgentWorkerProfile{}, false
	}
	return agent.WorkerProfiles[index], true
}

// runtimeVariants are the auto worker entries of a framework at one place:
// the plain one ("") and each profile, in the operator's order.
func runtimeVariants(agent core.SuperAgent, runtimeID string) []string {
	variants := []string{""}
	for _, profile := range agent.WorkerProfiles {
		if profile.RuntimeID == runtimeID {
			variants = append(variants, profile.ID)
		}
	}
	return variants
}

// autoRosterID names an auto worker entry: auto:<framework>[/<profile>][@<path>].
func autoRosterID(runtimeID, profile, pathID string) string {
	id := autoWorkerRosterID(profileLabel(runtimeID, profile))
	if pathID != "" {
		id += "@" + pathID
	}
	return id
}

// profileLabel names a framework and profile for people, as in claudecode/deep.
func profileLabel(runtimeID, profile string) string {
	if profile == "" {
		return runtimeID
	}
	return runtimeID + profileSep + profile
}

// withProfile gives an Agent instance id its profile.
func withProfile(agentID, profile string) string {
	if profile == "" {
		return agentID
	}
	return agentID + profileAgentSep + profile
}

// checkProfile reports whether a roster entry's profile is one the agent has.
func checkProfile(agent core.SuperAgent, workerID, runtimeID, profile string) error {
	if profile == "" {
		return nil
	}
	if _, ok := workerProfile(agent, runtimeID, profile); !ok {
		return fmt.Errorf("worker %q is not on the roster: %s has no profile %q", workerID, runtimeID, profile)
	}
	return nil
}

// applyProfile sets an auto worker's model settings from its profile. A
// setting the profile leaves empty keeps what the plain worker uses.
func applyProfile(instance *core.AgentInstance, profile core.SuperAgentWorkerProfile) {
	if profile.Model != "" {
		instance.DefaultModel = profile.Model
	}
	if profile.ReasoningEffort != "" {
		instance.DefaultReasoningEffort = profile.ReasoningEffort
	}
	if profile.ServiceTier != "" {
		instance.DefaultServiceTier = profile.ServiceTier
	}
}
