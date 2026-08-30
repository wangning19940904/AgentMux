package core

import (
	"context"
	"strings"
)

// resolveGuardInteraction evaluates enforceable native permission requests.
// It returns true only when allow/deny was successfully sent to the runtime;
// ask and evaluation failures continue through the existing human workflow.
func (e *Engine) resolveGuardInteraction(ctx context.Context, session AgentSession, event *Event, data map[string]string) bool {
	if e == nil || e.guard == nil || event == nil || event.Interaction == nil {
		return false
	}
	interaction := event.Interaction
	if interaction.Kind == AgentInteractionUserInput {
		return false
	}
	request := &GuardRequest{
		AgentID: strings.TrimSpace(data["agent_id"]), RuntimeID: strings.TrimSpace(data["runtime_id"]),
		Tool: "permission", Action: "request", Args: map[string]string{},
	}
	switch interaction.Kind {
	case AgentInteractionCommandApproval:
		request.Tool, request.Action = "shell", "execute"
	case AgentInteractionFileChangeApproval:
		request.Tool, request.Action = "file", "write"
	case AgentInteractionPermissionApproval:
		request.Tool, request.Action = "permission", "grant"
	}
	for key, value := range map[string]string{
		"command": interaction.Command,
		"cwd":     interaction.Cwd,
		"reason":  interaction.Reason,
	} {
		if value = strings.TrimSpace(value); value != "" {
			request.Args[key] = value
		}
	}
	decision, err := e.guard.Evaluate(ctx, request)
	if err != nil {
		e.log.Warn("guard evaluation failed; asking user", "interaction", interaction.ID, "err", err)
		return false
	}
	if decision == GuardAsk {
		return false
	}
	allow := decision == GuardAllow
	if decision != GuardAllow && decision != GuardDeny {
		e.log.Warn("guard returned unknown decision; asking user", "decision", decision)
		return false
	}
	var resolveErr error
	if interactive, ok := session.(InteractiveAgentSession); ok {
		value := "decline"
		if allow {
			value = "accept"
		}
		resolveErr = interactive.ResolveInteraction(ctx, interaction.ID, AgentInteractionResponse{Decision: value})
	} else {
		resolveErr = session.RespondPermission(ctx, allow)
	}
	if resolveErr != nil {
		e.log.Warn("guard could not resolve permission; asking user", "interaction", interaction.ID, "decision", decision, "err", resolveErr)
		return false
	}
	if event.Metadata == nil {
		event.Metadata = map[string]string{}
	}
	event.Metadata["guard_decision"] = string(decision)
	e.emit(ctx, HookPermission, map[string]string{
		"agent_id": request.AgentID, "runtime_id": request.RuntimeID,
		"interaction_id": interaction.ID, "guard_decision": string(decision),
	})
	return true
}
