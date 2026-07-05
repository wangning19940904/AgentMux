package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// ListAgentInstances returns all console-managed Agent instances.
func (s *Store) ListAgentInstances(ctx context.Context) ([]core.AgentInstance, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,runtime_id,work_dir,system_prompt,
		provider_tool,provider_id,memory_scope,env,channel_bindings,schedules,mcp_servers,
		skills,enabled,source,created_at,updated_at FROM agent_instances ORDER BY updated_at DESC, name`)
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
		provider_tool,provider_id,memory_scope,env,channel_bindings,schedules,mcp_servers,
		skills,enabled,source,created_at,updated_at FROM agent_instances WHERE id=?`, id)
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
	enabled := 0
	if a.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO agent_instances
		(id,name,runtime_id,work_dir,system_prompt,provider_tool,provider_id,memory_scope,
		 env,channel_bindings,schedules,mcp_servers,skills,enabled,source,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name,runtime_id=excluded.runtime_id,
		work_dir=excluded.work_dir,system_prompt=excluded.system_prompt,
		provider_tool=excluded.provider_tool,provider_id=excluded.provider_id,
		memory_scope=excluded.memory_scope,env=excluded.env,
		channel_bindings=excluded.channel_bindings,schedules=excluded.schedules,
		mcp_servers=excluded.mcp_servers,skills=excluded.skills,enabled=excluded.enabled,
		source=excluded.source,updated_at=excluded.updated_at`,
		a.ID, a.Name, a.RuntimeID, a.WorkDir, a.SystemPrompt, a.ProviderTool,
		a.ProviderID, a.MemoryScope, string(env), string(channels), string(schedules),
		string(mcpServers), string(skills), enabled, a.Source,
		a.CreatedAt.Format(time.RFC3339Nano), a.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// DeleteAgentInstance removes a console-managed Agent instance.
func (s *Store) DeleteAgentInstance(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agent_instances WHERE id=?`, id)
	return err
}

func scanAgentInstance(sc scanner) (core.AgentInstance, error) {
	var a core.AgentInstance
	var workDir, systemPrompt, providerTool, providerID, memoryScope sql.NullString
	var env, channels, schedules, mcpServers, skills, source, created, updated sql.NullString
	var enabled int
	if err := sc.Scan(&a.ID, &a.Name, &a.RuntimeID, &workDir, &systemPrompt,
		&providerTool, &providerID, &memoryScope, &env, &channels, &schedules,
		&mcpServers, &skills, &enabled, &source, &created, &updated); err != nil {
		return a, err
	}
	a.WorkDir = workDir.String
	a.SystemPrompt = systemPrompt.String
	a.ProviderTool = providerTool.String
	a.ProviderID = providerID.String
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
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return a, nil
}
