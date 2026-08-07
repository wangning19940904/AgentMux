package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/wangning19940904/AgentMux/core"
)

// ListAgentInstances returns all console-managed Agent instances.
func (s *Store) ListAgentInstances(ctx context.Context) ([]core.AgentInstance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,runtime_id,work_dir,system_prompt,
		provider_tool,provider_id,default_model,default_reasoning_effort,default_service_tier,default_approval_mode,memory_scope,env,channel_bindings,schedules,mcp_servers,
		skills,clis,enabled,source,created_at,updated_at FROM agent_instances ORDER BY updated_at DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.AgentInstance
	for rows.Next() {
		a, err := scanAgentInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAgentInstance returns one Agent instance or (nil,nil) if absent.
func (s *Store) GetAgentInstance(ctx context.Context, id string) (*core.AgentInstance, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,name,runtime_id,work_dir,system_prompt,
		provider_tool,provider_id,default_model,default_reasoning_effort,default_service_tier,default_approval_mode,memory_scope,env,channel_bindings,schedules,mcp_servers,
		skills,clis,enabled,source,created_at,updated_at FROM agent_instances WHERE id=?`, id)
	a, err := scanAgentInstance(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &a, err
}

// UpsertAgentInstance inserts or updates a product-level Agent instance.
func (s *Store) UpsertAgentInstance(ctx context.Context, a *core.AgentInstance) error {
	env, _ := json.Marshal(a.Env)
	channels, _ := json.Marshal(a.ChannelBindings)
	schedules, _ := json.Marshal(a.Schedules)
	mcpServers, _ := json.Marshal(a.MCPServers)
	skills, _ := json.Marshal(a.Skills)
	clis, _ := json.Marshal(a.CLIs)
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO agent_instances
		(id,name,runtime_id,work_dir,system_prompt,provider_tool,provider_id,memory_scope,
		 default_model,default_reasoning_effort,default_service_tier,default_approval_mode,env,channel_bindings,schedules,mcp_servers,skills,clis,enabled,source,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,runtime_id=excluded.runtime_id,
		work_dir=excluded.work_dir,system_prompt=excluded.system_prompt,
		provider_tool=excluded.provider_tool,provider_id=excluded.provider_id,
		memory_scope=excluded.memory_scope,default_model=excluded.default_model,
		default_reasoning_effort=excluded.default_reasoning_effort,default_service_tier=excluded.default_service_tier,
		default_approval_mode=excluded.default_approval_mode,env=excluded.env,
		channel_bindings=excluded.channel_bindings,schedules=excluded.schedules,
		mcp_servers=excluded.mcp_servers,skills=excluded.skills,clis=excluded.clis,enabled=excluded.enabled,
		source=excluded.source,updated_at=excluded.updated_at`,
		a.ID, a.Name, a.RuntimeID, a.WorkDir, a.SystemPrompt, a.ProviderTool,
		a.ProviderID, a.MemoryScope, a.DefaultModel, a.DefaultReasoningEffort, a.DefaultServiceTier, a.DefaultApprovalMode, string(env), string(channels), string(schedules),
		string(mcpServers), string(skills), string(clis), enabled, a.Source,
		a.CreatedAt.Format(time.RFC3339Nano), a.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteAgentInstance removes a console-managed Agent instance.
func (s *Store) DeleteAgentInstance(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM agent_instances WHERE id=?`, id)
	return err
}

// UpdateAgentRuntimeSettings persists defaults selected from a channel card.
// It intentionally does not touch active in-memory sessions: defaults apply
// only when future conversations create a new session.
func (s *Store) UpdateAgentRuntimeSettings(ctx context.Context, id string, settings core.RuntimeSettings) error {
	_, err := s.writer.ExecContext(ctx, `UPDATE agent_instances
		SET default_model=?, default_reasoning_effort=?, default_service_tier=?, default_approval_mode=?, updated_at=?
		WHERE id=?`, settings.Model, settings.ReasoningEffort, settings.ServiceTier, settings.ApprovalMode,
		time.Now().Format(time.RFC3339Nano), id)
	return err
}

func scanAgentInstance(sc scanner) (core.AgentInstance, error) {
	var a core.AgentInstance
	var workDir, systemPrompt, providerTool, providerID, defaultModel, defaultReasoningEffort, defaultServiceTier, defaultApprovalMode, memoryScope sql.NullString
	var env, channels, schedules, mcpServers, skills, clis, source, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&a.ID, &a.Name, &a.RuntimeID, &workDir, &systemPrompt,
		&providerTool, &providerID, &defaultModel, &defaultReasoningEffort, &defaultServiceTier, &defaultApprovalMode, &memoryScope, &env, &channels, &schedules,
		&mcpServers, &skills, &clis, &enabled, &source, &created, &updated); err != nil {
		return a, err
	}
	a.WorkDir = workDir.String
	a.SystemPrompt = systemPrompt.String
	a.ProviderTool = providerTool.String
	a.ProviderID = providerID.String
	a.DefaultModel = defaultModel.String
	a.DefaultReasoningEffort = defaultReasoningEffort.String
	a.DefaultServiceTier = defaultServiceTier.String
	a.DefaultApprovalMode = defaultApprovalMode.String
	a.MemoryScope = memoryScope.String
	a.Enabled = enabled != 0
	a.Source = source.String
	if env.String != "" {
		_ = json.Unmarshal([]byte(env.String), &a.Env)
	}
	if channels.String != "" {
		_ = json.Unmarshal([]byte(channels.String), &a.ChannelBindings)
	}
	if schedules.String != "" {
		_ = json.Unmarshal([]byte(schedules.String), &a.Schedules)
	}
	if mcpServers.String != "" {
		_ = json.Unmarshal([]byte(mcpServers.String), &a.MCPServers)
	}
	if skills.String != "" {
		_ = json.Unmarshal([]byte(skills.String), &a.Skills)
	}
	if clis.String != "" {
		_ = json.Unmarshal([]byte(clis.String), &a.CLIs)
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return a, nil
}
