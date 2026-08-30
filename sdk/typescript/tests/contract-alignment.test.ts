/**
 * Drift detection between the TypeScript types and contract/schemas goldens.
 * Runs only inside the AgentMux monorepo (the goldens are not shipped in the
 * npm package).
 */

import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const SCHEMAS_DIR = join(
  fileURLToPath(new URL(".", import.meta.url)),
  "..", "..", "..", "contract", "schemas",
);

// Wire fields the TypeScript interfaces expose, per golden schema.
const TYPE_FIELDS: Record<string, string[]> = {
	"invocation_request.json": ["agent_id", "conversation_id", "input", "attachments", "output_schema"],
  "invocation_result.json": [
    "id", "agent_id", "conversation_id", "session_id",
    "answer", "duration_ms", "usage",
  ],
  "invocation_stream_event.json": [
    "type", "invocation_id", "conversation_id", "session_id", "event_id",
    "turn_id", "item_id", "text", "status", "final", "duration_ms",
    "tool_name", "tool_call_id", "tool_input", "tool_result", "interaction",
    "usage", "metadata", "error", "result",
  ],
  "agent_instance.json": [
		"id", "name", "runtime_id", "desktop_thread_id", "work_dir", "workspace_mode",
		"worktree_base_ref", "session_backend", "system_prompt", "provider_tool", "provider_id",
		"provider_name", "default_model", "default_reasoning_effort", "default_service_tier",
		"default_approval_mode", "memory_scope", "env", "channel_bindings", "schedules",
		"mcp_servers", "skills", "clis", "enabled", "source", "owner_tenant_id",
		"owner_tenant_name", "visibility", "created_at", "updated_at",
  ],
  "channel.json": [
    "id", "name", "type", "enabled", "agent_id", "config",
		"owner_tenant_id", "owner_tenant_name", "visibility", "created_at", "updated_at",
  ],
	"tenant.json": ["id", "name", "status", "kind", "note", "created_at", "updated_at"],
  "trigger.json": [
		"id", "name", "kind", "enabled", "agent_id", "channel_id", "chat_id", "cron_expr",
		"prompt", "event", "action_type", "action_target", "token", "session_mode", "last_run",
		"last_status", "last_error", "owner_tenant_id", "created_at", "updated_at",
  ],
	"orchestration.json": ["id", "name", "status", "max_concurrency", "error", "tasks", "owner_tenant_id", "created_at", "started_at", "finished_at", "updated_at"],
  "orchestration_task.json": [
		"id", "orchestration_id", "input", "agent_id", "depends_on", "status", "output",
		"error", "invocation_id", "conversation_id", "created_at", "started_at", "finished_at", "updated_at",
  ],
	"turn_usage.json": ["Model", "RequestID", "RequestedModel", "ResolvedModel", "InputTokens", "OutputTokens", "CacheReadTokens", "CacheWriteTokens", "ReasoningTokens", "TotalTokens", "Cumulative", "Attempt", "TTFTMs", "DurationMs"],
};

describe.skipIf(!existsSync(SCHEMAS_DIR))("contract golden alignment", () => {
  it("covers every published schema", () => {
    for (const [schemaName, fields] of Object.entries(TYPE_FIELDS)) {
      const golden = JSON.parse(readFileSync(join(SCHEMAS_DIR, schemaName), "utf-8")) as {
        fields: Record<string, unknown>;
      };
			expect(Object.keys(golden.fields).sort(), `${schemaName} drifted`).toEqual([...fields].sort());
    }
  });
});
