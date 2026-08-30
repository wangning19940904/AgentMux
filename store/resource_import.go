package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/wangning19940904/AgentMux/core"
)

// ResourceImportSet is the normalized PostgreSQL resource set produced from
// one legacy config.toml file.
type ResourceImportSet struct {
	Agents   []core.AgentInstance
	Channels []core.Channel
	Triggers []core.Trigger
}

type ResourceImportCount struct {
	Create    int `json:"create"`
	Unchanged int `json:"unchanged"`
}

type ResourceImportConflict struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ResourceImportReport struct {
	DryRun    bool                     `json:"dry_run"`
	Applied   bool                     `json:"applied"`
	Agents    ResourceImportCount      `json:"agents"`
	Channels  ResourceImportCount      `json:"channels"`
	Triggers  ResourceImportCount      `json:"triggers"`
	Conflicts []ResourceImportConflict `json:"conflicts,omitempty"`
}

// ImportResources plans or atomically applies resources. Existing records are
// accepted only when their imported semantics are identical; the importer
// never overwrites a Console-managed record.
func (s *Store) ImportResources(ctx context.Context, resources ResourceImportSet, dryRun bool) (ResourceImportReport, error) {
	report := ResourceImportReport{DryRun: dryRun}
	newAgents := make([]core.AgentInstance, 0, len(resources.Agents))
	for _, item := range resources.Agents {
		existing, err := s.GetAgentInstance(ctx, item.ID)
		if err != nil {
			return report, err
		}
		if existing == nil {
			report.Agents.Create++
			newAgents = append(newAgents, item)
		} else if sameImportedAgent(*existing, item) {
			report.Agents.Unchanged++
		} else {
			report.Conflicts = append(report.Conflicts, ResourceImportConflict{Kind: "agent", ID: item.ID, Name: item.Name})
		}
	}
	newChannels := make([]core.Channel, 0, len(resources.Channels))
	for _, item := range resources.Channels {
		existing, err := s.GetChannel(ctx, item.ID)
		if err != nil {
			return report, err
		}
		if existing == nil {
			report.Channels.Create++
			newChannels = append(newChannels, item)
		} else if sameImportedChannel(*existing, item) {
			report.Channels.Unchanged++
		} else {
			report.Conflicts = append(report.Conflicts, ResourceImportConflict{Kind: "channel", ID: item.ID, Name: item.Name})
		}
	}
	newTriggers := make([]core.Trigger, 0, len(resources.Triggers))
	for _, item := range resources.Triggers {
		existing, err := s.GetTrigger(ctx, item.ID)
		if err != nil {
			return report, err
		}
		if existing == nil {
			report.Triggers.Create++
			newTriggers = append(newTriggers, item)
		} else if sameImportedTrigger(*existing, item) {
			report.Triggers.Unchanged++
		} else {
			report.Conflicts = append(report.Conflicts, ResourceImportConflict{Kind: "trigger", ID: item.ID, Name: item.Name})
		}
	}
	if dryRun {
		return report, nil
	}
	if len(report.Conflicts) > 0 {
		return report, fmt.Errorf("config import has %d conflict(s); no resources were written", len(report.Conflicts))
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer func() { _ = tx.Rollback() }()
	for index := range newAgents {
		if err := upsertAgentInstance(ctx, tx, &newAgents[index]); err != nil {
			return report, fmt.Errorf("import agent %q: %w", newAgents[index].Name, err)
		}
	}
	for index := range newChannels {
		if err := upsertChannel(ctx, tx, &newChannels[index]); err != nil {
			return report, fmt.Errorf("import channel %q: %w", newChannels[index].Name, err)
		}
	}
	for index := range newTriggers {
		if err := upsertTrigger(ctx, tx, &newTriggers[index]); err != nil {
			return report, fmt.Errorf("import trigger %q: %w", newTriggers[index].Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return report, err
	}
	report.Applied = true
	return report, nil
}

func sameImportedAgent(left, right core.AgentInstance) bool {
	return sameImportJSON(agentImportValue(left), agentImportValue(right))
}

func agentImportValue(item core.AgentInstance) any {
	return struct {
		ID, Name, RuntimeID, WorkDir, WorkspaceMode, WorktreeBaseRef, SessionBackend string
		SystemPrompt, ProviderTool, DefaultModel, MemoryScope, Source, Visibility    string
		Env                                                                          map[string]string
		Enabled                                                                      bool
	}{
		item.ID, item.Name, item.RuntimeID, item.WorkDir, item.WorkspaceMode, item.WorktreeBaseRef,
		item.SessionBackend, item.SystemPrompt, item.ProviderTool, item.DefaultModel, item.MemoryScope,
		item.Source, item.Visibility, item.Env, item.Enabled,
	}
}

func sameImportedChannel(left, right core.Channel) bool {
	return sameImportJSON(
		struct {
			ID, Name, Type, AgentID, Visibility string
			Config                              map[string]string
			Enabled                             bool
		}{left.ID, left.Name, left.Type, left.AgentID, left.Visibility, left.Config, left.Enabled},
		struct {
			ID, Name, Type, AgentID, Visibility string
			Config                              map[string]string
			Enabled                             bool
		}{right.ID, right.Name, right.Type, right.AgentID, right.Visibility, right.Config, right.Enabled},
	)
}

func sameImportedTrigger(left, right core.Trigger) bool {
	return sameImportJSON(
		struct {
			ID, Name, Kind, AgentID, Event, ActionType, ActionTarget string
			Enabled                                                  bool
		}{left.ID, left.Name, left.Kind, left.AgentID, left.Event, left.ActionType, left.ActionTarget, left.Enabled},
		struct {
			ID, Name, Kind, AgentID, Event, ActionType, ActionTarget string
			Enabled                                                  bool
		}{right.ID, right.Name, right.Kind, right.AgentID, right.Event, right.ActionType, right.ActionTarget, right.Enabled},
	)
}

func sameImportJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
