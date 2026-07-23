package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/agentnexus/agentnexus/core"
)

// --- Memory (AgentNexus Memory) ---

// PutMemory inserts or updates a memory entry.
func (s *Store) PutMemory(ctx context.Context, e *core.MemoryEntry) error {
	tags := strings.Join(e.Tags, ",")
	meta, _ := json.Marshal(e.Meta)
	_, err := s.writer.ExecContext(ctx, `INSERT INTO memory_entries
		(id,scope,content,tags,meta,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET scope=excluded.scope,content=excluded.content,
		tags=excluded.tags,meta=excluded.meta,updated_at=excluded.updated_at`,
		e.ID, e.Scope, e.Content, tags, string(meta),
		e.CreatedAt.Format(time.RFC3339Nano), e.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

// GetMemory returns one entry or (nil,nil) if absent.
func (s *Store) GetMemory(ctx context.Context, id string) (*core.MemoryEntry, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,scope,content,tags,meta,created_at,updated_at
		FROM memory_entries WHERE id=?`, id)
	e, err := scanMemory(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// SearchMemory returns entries within scope matching query (LIKE on content).
func (s *Store) SearchMemory(ctx context.Context, scope, query string, limit int) ([]*core.MemoryEntry, error) {
	q := `SELECT id,scope,content,tags,meta,created_at,updated_at FROM memory_entries`
	var args []any
	var conds []string
	if scope != "" {
		conds = append(conds, "scope=?")
		args = append(args, scope)
	}
	if query != "" {
		conds = append(conds, "content LIKE ?")
		args = append(args, "%"+query+"%")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY updated_at DESC"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*core.MemoryEntry
	for rows.Next() {
		e, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteMemory removes a memory entry by id.
func (s *Store) DeleteMemory(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM memory_entries WHERE id=?`, id)
	return err
}

func scanMemory(sc scanner) (*core.MemoryEntry, error) {
	var e core.MemoryEntry
	var tags, meta, created, updated sql.NullString
	if err := sc.Scan(&e.ID, &e.Scope, &e.Content, &tags, &meta, &created, &updated); err != nil {
		return nil, err
	}
	if tags.String != "" {
		e.Tags = strings.Split(tags.String, ",")
	}
	if meta.String != "" {
		_ = json.Unmarshal([]byte(meta.String), &e.Meta)
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, created.String)
	e.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated.String)
	return &e, nil
}

// --- MCP Registry (AgentNexus MCP Registry) ---

// ListMCPServers returns all registered MCP servers ordered by name.
func (s *Store) ListMCPServers(ctx context.Context) ([]core.MCPServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,transport,command,args,url,env,enabled
		FROM mcp_servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MCPServer
	for rows.Next() {
		var m core.MCPServer
		var command, args, url, env sql.NullString
		var enabled int
		if err := rows.Scan(&m.Name, &m.Transport, &command, &args, &url, &env, &enabled); err != nil {
			return nil, err
		}
		m.Command = command.String
		if args.String != "" {
			_ = json.Unmarshal([]byte(args.String), &m.Args)
		}
		m.URL = url.String
		if env.String != "" {
			_ = json.Unmarshal([]byte(env.String), &m.Env)
		}
		m.Enabled = enabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertMCPServer inserts or updates an MCP server definition.
func (s *Store) UpsertMCPServer(ctx context.Context, m *core.MCPServer) error {
	args, _ := json.Marshal(m.Args)
	env, _ := json.Marshal(m.Env)
	enabled := 0
	if m.Enabled {
		enabled = 1
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO mcp_servers
		(name,transport,command,args,url,env,enabled) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET transport=excluded.transport,command=excluded.command,
		args=excluded.args,url=excluded.url,env=excluded.env,enabled=excluded.enabled`,
		m.Name, m.Transport, m.Command, string(args), m.URL, string(env), enabled)
	return err
}

// DeleteMCPServer removes an MCP server by name.
func (s *Store) DeleteMCPServer(ctx context.Context, name string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM mcp_servers WHERE name=?`, name)
	return err
}

// --- Guard (AgentNexus Guard) ---

// GuardPolicy is a single stored policy rule.
type GuardPolicy struct {
	ID       string `json:"id"`
	Tool     string `json:"tool"`
	Action   string `json:"action,omitempty"`
	Decision string `json:"decision"`
	Priority int    `json:"priority"`
}

// ListGuardPolicies returns all policies ordered by descending priority.
func (s *Store) ListGuardPolicies(ctx context.Context) ([]GuardPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,tool,action,decision,priority
		FROM guard_policies ORDER BY priority DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GuardPolicy
	for rows.Next() {
		var p GuardPolicy
		var action sql.NullString
		if err := rows.Scan(&p.ID, &p.Tool, &action, &p.Decision, &p.Priority); err != nil {
			return nil, err
		}
		p.Action = action.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertGuardPolicy inserts or updates a guard policy.
func (s *Store) UpsertGuardPolicy(ctx context.Context, p *GuardPolicy) error {
	_, err := s.writer.ExecContext(ctx, `INSERT INTO guard_policies
		(id,tool,action,decision,priority) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET tool=excluded.tool,action=excluded.action,
		decision=excluded.decision,priority=excluded.priority`,
		p.ID, p.Tool, p.Action, p.Decision, p.Priority)
	return err
}

// DeleteGuardPolicy removes a guard policy by id.
func (s *Store) DeleteGuardPolicy(ctx context.Context, id string) error {
	_, err := s.writer.ExecContext(ctx, `DELETE FROM guard_policies WHERE id=?`, id)
	return err
}
