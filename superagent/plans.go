package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wangning19940904/AgentMux/core"
)

// Plans, cost tiers and remembered trust.
//
// Every roster entry has a cost tier, low, medium or high, from its
// framework and model, which the operator can override on a roster worker.
// The brain may propose several Tickets at once with a plan action; nothing
// starts until the requester's side approves it in the Console. An agent's
// plan_approval setting says when an accept must be a plan instead: off (the
// default: accept starts at once, as before), always, or above a tier. When
// a plan is approved with "don't ask again", the requester's highest approved
// tier is remembered for the agent, and later work at or under it starts
// without a card. Plans and trust are kept in the store's settings, never in
// the agent's home, which its brain can write.

const (
	actionPlan = "plan"

	maxPlanTickets   = 8
	maxPlanSummary   = 2000
	planTTL          = 7 * 24 * time.Hour
	maxPlansKept     = 50
	maxPlansShown    = 10
	plansSettingKey  = "super_agent_plans:"
	trustSettingKey  = "super_agent_trust:"
	trustDecidedBy   = "remembered trust"
	planEventTitle   = "Plan decision"
	consoleDecidedBy = "Console"
)

// tierRank orders tiers; an unknown tier ranks 0.
func tierRank(tier string) int {
	switch tier {
	case core.CostTierLow:
		return 1
	case core.CostTierMedium:
		return 2
	case core.CostTierHigh:
		return 3
	}
	return 0
}

func checkTier(tier string) error {
	if tier != "" && tierRank(tier) == 0 {
		return fmt.Errorf("cost tier must be %s, %s or %s", core.CostTierLow, core.CostTierMedium, core.CostTierHigh)
	}
	return nil
}

// defaultCostTier is a worker's tier from its framework and model. A model
// name says most: the small ones (haiku, mini, flash, …) are low and the
// largest (opus, pro, ultra, …) high. Without a model, the framework's
// default model decides.
func defaultCostTier(runtime, model string) string {
	model = strings.ToLower(model)
	for _, word := range []string{"haiku", "mini", "nano", "flash", "lite", "small"} {
		if strings.Contains(model, word) {
			return core.CostTierLow
		}
	}
	for _, word := range []string{"opus", "-pro", "ultra", "max", "o3"} {
		if strings.Contains(model, word) {
			return core.CostTierHigh
		}
	}
	if model != "" {
		return core.CostTierMedium
	}
	switch strings.ToLower(runtime) {
	case "claude", "claudecode", "claude-code":
		// Claude Code's default model is its largest.
		return core.CostTierHigh
	}
	return core.CostTierMedium
}

// placeTier is a roster entry's cost tier.
func (s *Service) placeTier(ctx context.Context, agent core.SuperAgent, place workerPlace) string {
	if place.Roster {
		worker := agent.Workers[place.index]
		if worker.CostTier != "" {
			return worker.CostTier
		}
		model := ""
		if worker.HostID == "" {
			if instance, err := s.store.GetAgentInstance(ctx, worker.AgentID); err == nil && instance != nil {
				model = instance.DefaultModel
			}
		}
		return defaultCostTier(place.Runtime, model)
	}
	model := ""
	// A worker of the router's own framework on this machine uses its model.
	if place.HostID == "" {
		if router, err := s.store.GetAgentInstance(ctx, agent.RouterAgentID); err == nil && router != nil && router.RuntimeID == place.Runtime {
			model = router.DefaultModel
		}
	}
	return defaultCostTier(place.Runtime, model)
}

// rosterTiers are the cost tiers of every roster entry, by roster id.
func (s *Service) rosterTiers(ctx context.Context, agent core.SuperAgent, statuses []core.SuperAgentWorkerStatus) map[string]string {
	tiers := map[string]string{}
	for index, worker := range agent.Workers {
		place := workerPlace{WorkerID: worker.ID, HostID: worker.HostID, Roster: true, index: index}
		if index < len(statuses) {
			place.Runtime = statuses[index].RuntimeID
		}
		tiers[worker.ID] = s.placeTier(ctx, agent, place)
	}
	if !agent.AutoWorkers {
		return tiers
	}
	for _, runtimeID := range agent.WorkerRuntimes {
		tiers[autoWorkerRosterID(runtimeID)] = s.placeTier(ctx, agent, workerPlace{Runtime: runtimeID})
		for _, machine := range agent.WorkerMachines {
			tiers[machineWorkerRosterID(runtimeID, machinePathID(machine))] = s.placeTier(ctx, agent, workerPlace{Runtime: runtimeID, HostID: machine.HostID})
		}
	}
	return tiers
}

func normalizePlanApproval(agent *core.SuperAgent) error {
	mode := strings.ToLower(strings.TrimSpace(agent.PlanApproval))
	switch mode {
	case "", "off":
		agent.PlanApproval = ""
	case core.PlanApprovalAlways, core.PlanApprovalAboveLow, core.PlanApprovalAboveMedium:
		agent.PlanApproval = mode
	default:
		return fmt.Errorf("plan_approval must be off, %s, %s or %s", core.PlanApprovalAlways, core.PlanApprovalAboveLow, core.PlanApprovalAboveMedium)
	}
	for index := range agent.Workers {
		agent.Workers[index].CostTier = strings.ToLower(strings.TrimSpace(agent.Workers[index].CostTier))
		if err := checkTier(agent.Workers[index].CostTier); err != nil {
			return fmt.Errorf("worker %q: %w", firstNonEmpty(agent.Workers[index].Label, agent.Workers[index].AgentID), err)
		}
	}
	return nil
}

// planRequired reports whether work of a tier needs a plan under a mode.
func planRequired(mode, tier string) bool {
	switch mode {
	case core.PlanApprovalAlways:
		return true
	case core.PlanApprovalAboveLow:
		return tierRank(tier) > tierRank(core.CostTierLow)
	case core.PlanApprovalAboveMedium:
		return tierRank(tier) > tierRank(core.CostTierMedium)
	}
	return false
}

// requester is who the work is for: the event's sender, or, for AgentMux's
// own news about a Ticket, whoever asked for that Ticket.
func (s *Service) requester(ctx context.Context, agentID string, trigger core.SuperAgentTrigger) (id, name string) {
	if trigger.Kind == "" || trigger.Kind == core.SuperAgentTriggerPlan {
		return strings.TrimSpace(trigger.Actor), firstNonEmpty(trigger.ActorName, trigger.Actor)
	}
	if trigger.TicketID != "" {
		if ticket, err := s.agentTicket(ctx, agentID, trigger.TicketID); err == nil && ticket.Actor != "agentmux" {
			return ticket.Actor, ticket.Actor
		}
	}
	return "", ""
}

// approvalProblem says why work of a tier for a requester needs a plan, or "".
func (s *Service) approvalProblem(ctx context.Context, agent core.SuperAgent, requester, tier string) string {
	if !planRequired(agent.PlanApproval, tier) {
		return ""
	}
	if requester != "" && tierRank(s.trustedTier(ctx, agent.ID, requester)) >= tierRank(tier) {
		return ""
	}
	return fmt.Sprintf("this agent needs the requester's approval before %s-tier work starts: propose it with a \"plan\" action (the same fields under \"tickets\") instead of accept", tier)
}

// ---- storage ----

func (s *Service) loadSetting(ctx context.Context, key string, into any) error {
	raw, ok, err := s.store.GetSetting(ctx, key)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return err
	}
	return json.Unmarshal([]byte(raw), into)
}

func (s *Service) saveSetting(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return s.store.SetSetting(ctx, key, string(data))
}

// loadPlans returns an agent's plans, newest first; a pending plan past
// planTTL is expired. Callers hold plansMu.
func (s *Service) loadPlans(ctx context.Context, agentID string) ([]core.SuperAgentPlan, error) {
	var plans []core.SuperAgentPlan
	if err := s.loadSetting(ctx, plansSettingKey+agentID, &plans); err != nil {
		return nil, err
	}
	now := s.now()
	for index := range plans {
		if plans[index].Status == core.SuperAgentPlanPending && now.Sub(plans[index].CreatedAt) > planTTL {
			plans[index].Status, plans[index].DecidedAt = core.SuperAgentPlanExpired, now.UTC()
		}
	}
	return plans, nil
}

func (s *Service) savePlans(ctx context.Context, agentID string, plans []core.SuperAgentPlan) error {
	slices.SortStableFunc(plans, func(a, b core.SuperAgentPlan) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(plans) > maxPlansKept {
		plans = plans[:maxPlansKept]
	}
	return s.saveSetting(ctx, plansSettingKey+agentID, plans)
}

// Trust lists an agent's remembered approvals.
func (s *Service) Trust(ctx context.Context, agentID string) ([]core.SuperAgentTrust, error) {
	var trust []core.SuperAgentTrust
	err := s.loadSetting(ctx, trustSettingKey+agentID, &trust)
	return trust, err
}

func (s *Service) trustedTier(ctx context.Context, agentID, requester string) string {
	trust, err := s.Trust(ctx, agentID)
	if err != nil {
		return ""
	}
	for _, entry := range trust {
		if entry.Requester == requester {
			return entry.Tier
		}
	}
	return ""
}

// remember stores a requester's approval up to tier, keeping a higher one.
func (s *Service) remember(ctx context.Context, agentID, requester, name, tier, by string) error {
	trust, err := s.Trust(ctx, agentID)
	if err != nil {
		return err
	}
	entry := core.SuperAgentTrust{Requester: requester, RequesterName: name, Tier: tier, ApprovedBy: by, At: s.now().UTC()}
	if index := slices.IndexFunc(trust, func(existing core.SuperAgentTrust) bool { return existing.Requester == requester }); index >= 0 {
		if tierRank(trust[index].Tier) > tierRank(tier) {
			entry.Tier = trust[index].Tier
		}
		trust[index] = entry
	} else {
		trust = append(trust, entry)
	}
	return s.saveSetting(ctx, trustSettingKey+agentID, trust)
}

// RevokeTrust forgets a requester's remembered approval.
func (s *Service) RevokeTrust(ctx context.Context, agentID, requester string) error {
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	trust, err := s.Trust(ctx, agentID)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(trust, func(entry core.SuperAgentTrust) bool { return entry.Requester == requester })
	if len(kept) == len(trust) {
		return ErrNotFound
	}
	return s.saveSetting(ctx, trustSettingKey+agentID, kept)
}

// Plans lists an agent's plans for the Console, newest first, without the
// events they answer.
func (s *Service) Plans(ctx context.Context, agentID string) ([]core.SuperAgentPlan, error) {
	s.plansMu.Lock()
	plans, err := s.loadPlans(ctx, agentID)
	s.plansMu.Unlock()
	for index := range plans {
		plans[index].Trigger = nil
	}
	return plans, err
}

// ---- proposing ----

// checkPlanShape checks a plan action's own fields.
func checkPlanShape(action routerAction) error {
	if len(action.Tickets) == 0 {
		return errors.New("a plan action needs \"tickets\"")
	}
	if len(action.Tickets) > maxPlanTickets {
		return fmt.Errorf("a plan has at most %d tickets", maxPlanTickets)
	}
	refs := map[string]bool{}
	for index, item := range action.Tickets {
		if strings.TrimSpace(item.WorkerID) == "" || strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("plan ticket %d needs worker_id and title", index+1)
		}
		if err := checkMachinePolicy(item.MachinePolicy); err != nil {
			return err
		}
		if ref := strings.TrimSpace(item.Ref); ref != "" {
			if refs[ref] {
				return fmt.Errorf("plan ref %q is used twice", ref)
			}
			refs[ref] = true
		}
	}
	return nil
}

// planAccepts are a plan's Tickets as accepts.
func planAccepts(items []routerAction, approved bool) []routerAction {
	accepts := make([]routerAction, 0, len(items))
	for _, item := range items {
		item.Action = actionAccept
		item.Tickets, item.placed = nil, nil
		item.approved = approved
		accepts = append(accepts, item)
	}
	return accepts
}

// preflightPlan checks every Ticket of a plan as an accept would be, except
// for approval, and says where each would run now and at what tier.
func (s *Service) preflightPlan(ctx context.Context, agent core.SuperAgent, action routerAction, claims *claimCheck, resolve func(string) (string, error)) ([]core.SuperAgentPlanItem, string, error) {
	local := map[string]bool{}
	var items []core.SuperAgentPlanItem
	highest := ""
	for index, item := range planAccepts(action.Tickets, true) {
		if err := s.checkWorker(agent, item.WorkerID); err != nil {
			return nil, "", fmt.Errorf("plan ticket %d: %w", index+1, err)
		}
		if err := checkCriteria(item.DoneWhen, item.ExpectMinutes); err != nil {
			return nil, "", fmt.Errorf("plan ticket %d: %w", index+1, err)
		}
		for _, id := range actionDependencies(item) {
			if local[id] {
				continue
			}
			status, err := resolve(id)
			if err != nil {
				return nil, "", fmt.Errorf("plan ticket %d: %w", index+1, err)
			}
			if status == core.SuperAgentTicketStopped {
				return nil, "", fmt.Errorf("plan ticket %d: ticket %s was stopped, so nothing can depend on it", index+1, id)
			}
		}
		placed, err := s.place(ctx, agent, item, func(place workerPlace) string { return claims.conflict(ctx, item, place) })
		if err != nil {
			return nil, "", fmt.Errorf("plan ticket %d: %w", index+1, err)
		}
		claims.claim(ctx, item, placed.place)
		tier := s.placeTier(ctx, agent, placed.place)
		if tierRank(tier) > tierRank(highest) {
			highest = tier
		}
		entry := core.SuperAgentPlanItem{
			Ref: strings.TrimSpace(item.Ref), Title: strings.TrimSpace(item.Title), WorkerID: item.WorkerID,
			Instructions: strings.TrimSpace(item.Instructions), DoneWhen: cleanCriteria(item.DoneWhen), ExpectMinutes: item.ExpectMinutes,
			DependsOn: item.DependsOn, BaseTicket: item.BaseTicket, MachinePolicy: item.MachinePolicy,
			Tier: tier, Where: s.machineLabel(placed.place.HostID),
		}
		if placed.moved != nil {
			entry.PlacedWorkerID = placed.moved.WorkerID
		}
		items = append(items, entry)
		if entry.Ref != "" {
			local[entry.Ref] = true
		}
	}
	return items, highest, nil
}

// proposePlan stores a plan for approval, or starts it at once when the
// requester already trusts the agent with work of its tier. It returns what
// AgentMux tells the thread.
func (s *Service) proposePlan(ctx context.Context, agent core.SuperAgent, trigger core.SuperAgentTrigger, action routerAction) (core.SuperAgentPlan, string, error) {
	requester, name := s.requester(ctx, agent.ID, trigger)
	stored := trigger
	plan := core.SuperAgentPlan{
		ID: "pln_" + uuid.NewString(), SuperAgentID: agent.ID,
		Title: firstNonEmpty(strings.TrimSpace(action.Title), "Plan"), Summary: truncateRunes(strings.TrimSpace(firstNonEmpty(action.Text, action.Instructions)), maxPlanSummary),
		Requester: requester, RequesterName: name, ThreadKey: trigger.ThreadKey, TriggerID: trigger.ID,
		Tickets: action.planItems, Tier: action.planTier, Status: core.SuperAgentPlanPending,
		CreatedAt: s.now().UTC(), Trigger: &stored,
	}
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	if requester != "" && tierRank(s.trustedTier(ctx, agent.ID, requester)) >= tierRank(plan.Tier) {
		ids, err := s.startPlan(ctx, agent, plan)
		if err != nil {
			return plan, "", err
		}
		plan.Status, plan.DecidedBy, plan.DecidedAt, plan.TicketIDs = core.SuperAgentPlanApproved, trustDecidedBy, s.now().UTC(), ids
		plans, err := s.loadPlans(ctx, agent.ID)
		if err == nil {
			err = s.savePlans(ctx, agent.ID, append(plans, plan))
		}
		return plan, fmt.Sprintf("Started plan “%s” (%d tickets): %s approved %s-tier work earlier.", plan.Title, len(ids), firstNonEmpty(name, requester), s.trustedTier(ctx, agent.ID, requester)), err
	}
	plans, err := s.loadPlans(ctx, agent.ID)
	if err != nil {
		return plan, "", err
	}
	if err := s.savePlans(ctx, agent.ID, append(plans, plan)); err != nil {
		return plan, "", err
	}
	return plan, planText(plan), nil
}

// planText is the plan as the thread it came from reads it.
func planText(plan core.SuperAgentPlan) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Proposed plan “%s” (%s tier), waiting for approval in the AgentMux Console (Agents → this agent → Plans).", plan.Title, plan.Tier)
	if plan.Summary != "" {
		text.WriteString("\n" + plan.Summary)
	}
	for index, item := range plan.Tickets {
		worker := item.WorkerID
		if item.PlacedWorkerID != "" {
			worker = item.PlacedWorkerID + " (instead of " + item.WorkerID + ")"
		}
		fmt.Fprintf(&text, "\n%d. %s: %s on %s, %s tier", index+1, item.Title, worker, item.Where, item.Tier)
		if len(item.DependsOn) > 0 {
			text.WriteString(", after " + strings.Join(item.DependsOn, ", "))
		}
	}
	return text.String()
}

// startPlan starts a plan's Tickets through the normal accept path: the same
// checks, then the Tickets in order, with refs resolved. Callers hold plansMu.
func (s *Service) startPlan(ctx context.Context, agent core.SuperAgent, plan core.SuperAgentPlan) ([]string, error) {
	if plan.Trigger == nil {
		return nil, errors.New("this plan no longer has the request it answers")
	}
	trigger := *plan.Trigger
	items := make([]routerAction, 0, len(plan.Tickets))
	for _, item := range plan.Tickets {
		items = append(items, routerAction{
			Ref: item.Ref, WorkerID: item.WorkerID, Title: item.Title, Instructions: item.Instructions,
			DoneWhen: item.DoneWhen, ExpectMinutes: item.ExpectMinutes, DependsOn: item.DependsOn,
			BaseTicket: item.BaseTicket, MachinePolicy: item.MachinePolicy,
		})
	}
	decision := routerDecision{Actions: planAccepts(items, true)}
	if err := s.preflight(ctx, agent, trigger, decision); err != nil {
		return nil, err
	}
	refs := map[string]string{}
	resolve := func(id string) string {
		id = strings.TrimSpace(id)
		if ticketID, ok := refs[id]; ok {
			return ticketID
		}
		return id
	}
	var ids []string
	for _, action := range decision.Actions {
		var dependsOn []string
		for _, id := range action.DependsOn {
			if id = resolve(id); id != "" {
				dependsOn = append(dependsOn, id)
			}
		}
		action.DependsOn, action.BaseTicket = dependsOn, resolve(action.BaseTicket)
		id, err := s.accept(ctx, agent, trigger, action)
		if err != nil {
			return ids, fmt.Errorf("started %d of %d tickets: %w", len(ids), len(decision.Actions), err)
		}
		ids = append(ids, id)
		if ref := strings.TrimSpace(action.Ref); ref != "" {
			refs[ref] = id
		}
	}
	return ids, nil
}

// ---- deciding ----

// ApprovePlan starts a pending plan's Tickets. With remember, the requester's
// approval of work up to the plan's tier is kept for this agent. A plan
// whose Tickets can no longer start stays pending, and the error says why.
func (s *Service) ApprovePlan(ctx context.Context, agentID, planID string, remember bool, by string) (core.SuperAgentPlan, error) {
	by = firstNonEmpty(strings.TrimSpace(by), consoleDecidedBy)
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	agent, plans, index, err := s.pendingPlan(ctx, agentID, planID)
	if err != nil {
		return core.SuperAgentPlan{}, err
	}
	plan := plans[index]
	ids, err := s.startPlan(ctx, agent, plan)
	if err != nil && len(ids) == 0 {
		return core.SuperAgentPlan{}, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	plan.Status, plan.DecidedBy, plan.DecidedAt, plan.TicketIDs = core.SuperAgentPlanApproved, by, s.now().UTC(), ids
	if err != nil {
		plan.Note = err.Error()
	}
	plans[index] = plan
	if saveErr := s.savePlans(ctx, agent.ID, plans); saveErr != nil {
		return plan, saveErr
	}
	if remember && plan.Requester != "" {
		if err := s.remember(ctx, agent.ID, plan.Requester, plan.RequesterName, plan.Tier, by); err != nil {
			return plan, err
		}
	}
	// The thread hears it from AgentMux; the brain sees the Tickets with its
	// next event, without a turn of its own.
	trigger := *plan.Trigger
	message := fmt.Sprintf("Plan “%s” approved: started %d tickets.", plan.Title, len(ids))
	if plan.Note != "" {
		message += " " + plan.Note
	}
	if sendErr := s.sendReply(ctx, agent, trigger.SourceID, trigger.Reply, message); sendErr != nil {
		s.log.Warn("tell the thread a plan was approved", "plan", plan.ID, "err", sendErr)
	}
	plan.Trigger = nil
	return plan, nil
}

// DeclinePlan withdraws a pending plan and tells the brain, on the plan's
// thread, so it can answer the requester.
func (s *Service) DeclinePlan(ctx context.Context, agentID, planID, note, by string) (core.SuperAgentPlan, error) {
	by = firstNonEmpty(strings.TrimSpace(by), consoleDecidedBy)
	s.plansMu.Lock()
	defer s.plansMu.Unlock()
	agent, plans, index, err := s.pendingPlan(ctx, agentID, planID)
	if err != nil {
		return core.SuperAgentPlan{}, err
	}
	plan := plans[index]
	plan.Status, plan.DecidedBy, plan.DecidedAt, plan.Note = core.SuperAgentPlanDeclined, by, s.now().UTC(), truncateRunes(strings.TrimSpace(note), 1000)
	plans[index] = plan
	if err := s.savePlans(ctx, agent.ID, plans); err != nil {
		return plan, err
	}
	text := fmt.Sprintf("The plan %s (“%s”) you proposed was declined in the Console; nothing was started.", plan.ID, plan.Title)
	if plan.Note != "" {
		text += " Note: " + plan.Note
	}
	if err := s.insertPlanEvent(ctx, agent, plan, text); err != nil {
		return plan, err
	}
	plan.Trigger = nil
	return plan, nil
}

func (s *Service) pendingPlan(ctx context.Context, agentID, planID string) (core.SuperAgent, []core.SuperAgentPlan, int, error) {
	stored, err := s.store.GetSuperAgent(ctx, agentID)
	if err != nil {
		return core.SuperAgent{}, nil, 0, err
	}
	if stored == nil {
		return core.SuperAgent{}, nil, 0, ErrNotFound
	}
	agent := s.withWorkerRuntimes(ctx, *stored)
	plans, err := s.loadPlans(ctx, agentID)
	if err != nil {
		return agent, nil, 0, err
	}
	index := slices.IndexFunc(plans, func(plan core.SuperAgentPlan) bool { return plan.ID == strings.TrimSpace(planID) })
	if index < 0 {
		return agent, nil, 0, ErrNotFound
	}
	if plans[index].Status != core.SuperAgentPlanPending {
		return agent, nil, 0, conflictError("plan " + planID + " is " + plans[index].Status)
	}
	return agent, plans, index, nil
}

// insertPlanEvent tells the brain about a plan decision on the plan's thread.
// The requester is its actor, so work the brain starts in answer is theirs.
func (s *Service) insertPlanEvent(ctx context.Context, agent core.SuperAgent, plan core.SuperAgentPlan, text string) error {
	if plan.Trigger == nil {
		return nil
	}
	now := s.now().UTC()
	trigger := core.SuperAgentTrigger{
		ID: "sat_" + uuid.NewString(), SuperAgentID: agent.ID, SourceID: plan.Trigger.SourceID, SourceKind: plan.Trigger.SourceKind,
		DedupeKey: "plan:" + plan.ID + ":" + plan.Status, ThreadKey: plan.ThreadKey,
		Actor: plan.Requester, ActorName: plan.RequesterName, Addressed: true, Kind: core.SuperAgentTriggerPlan,
		Title: planEventTitle, Text: text, Reply: plan.Trigger.Reply,
		Disposition: core.SuperAgentDispositionPending, ReceivedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.InsertSuperAgentTrigger(ctx, trigger); err != nil {
		return err
	}
	s.notify()
	return nil
}

// planView is a pending plan as the brain is shown it.
type planView struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Tier      string `json:"tier"`
	Requester string `json:"requester,omitempty"`
	Thread    string `json:"thread"`
	Proposed  string `json:"proposed"`
	Tickets   int    `json:"tickets"`
}

func (s *Service) pendingPlanViews(ctx context.Context, agentID, threadKey string, names map[string]string) []planView {
	s.plansMu.Lock()
	plans, err := s.loadPlans(ctx, agentID)
	s.plansMu.Unlock()
	if err != nil {
		return nil
	}
	now := s.now()
	var out []planView
	for _, plan := range plans {
		if plan.Status != core.SuperAgentPlanPending || len(out) >= maxPlansShown {
			continue
		}
		view := planView{ID: plan.ID, Title: plan.Title, Tier: plan.Tier, Requester: plan.RequesterName, Thread: "this thread", Proposed: ago(now, plan.CreatedAt), Tickets: len(plan.Tickets)}
		if plan.ThreadKey != threadKey {
			view.Thread = describeThread(plan.ThreadKey, "", names)
		}
		out = append(out, view)
	}
	return out
}

// planNote explains plans to the brain of an agent that asks for approval.
func planNote(agent core.SuperAgent, trusted string) string {
	when := "before any work starts"
	switch agent.PlanApproval {
	case core.PlanApprovalAboveLow:
		when = "before medium- or high-tier work starts"
	case core.PlanApprovalAboveMedium:
		when = "before high-tier work starts"
	}
	note := `This agent needs the requester's approval ` + when + ` (each roster entry has a "cost_tier"). Propose such work with one block like {"action":"plan","title":"…","text":"why, in a sentence or two","tickets":[{"ref":"a","worker_id":"","title":"","instructions":"","done_when":[],"depends_on":[],"machine_policy":""}]}: AgentMux shows it to the requester and starts the tickets only when they approve; refs work in depends_on inside the plan. An accept that needs approval is refused. Say in your reply that a plan awaits approval.`
	if trusted != "" {
		note += " This requester already approved work up to " + trusted + " tier, which starts without a plan."
	}
	return note
}
