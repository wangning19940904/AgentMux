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
  "invocation_result.json": [
    "id", "agent_id", "project", "conversation_id", "session_id",
    "answer", "duration_ms", "usage",
  ],
  "invocation_stream_event.json": [
    "type", "invocation_id", "conversation_id", "session_id", "event_id",
    "turn_id", "item_id", "text", "status", "final", "duration_ms",
    "tool_name", "tool_call_id", "tool_input", "tool_result", "interaction",
    "usage", "metadata", "error", "result",
  ],
  "agent_instance.json": [
    "id", "name", "runtime_id", "enabled", "work_dir", "system_prompt",
    "provider_tool", "default_model", "default_reasoning_effort",
    "default_service_tier", "default_approval_mode", "source",
    "owner_tenant_id", "owner_tenant_name", "visibility",
  ],
  "channel.json": [
    "id", "name", "type", "enabled", "agent_id", "config",
    "owner_tenant_id", "owner_tenant_name", "visibility",
  ],
  "tenant.json": ["id", "name", "status", "kind", "note"],
  "trigger.json": [
    "id", "name", "kind", "enabled", "agent_id", "channel_id", "cron_expr",
    "prompt", "event", "last_status", "last_error",
  ],
  "orchestration.json": ["id", "name", "status", "max_concurrency", "error", "tasks"],
  "orchestration_task.json": [
    "id", "input", "agent_id", "project", "depends_on", "status", "output",
    "error", "invocation_id", "conversation_id",
  ],
};

describe.skipIf(!existsSync(SCHEMAS_DIR))("contract golden alignment", () => {
  it("covers every published schema", () => {
    for (const [schemaName, fields] of Object.entries(TYPE_FIELDS)) {
      const golden = JSON.parse(readFileSync(join(SCHEMAS_DIR, schemaName), "utf-8")) as {
        fields: Record<string, unknown>;
      };
      const goldenFields = new Set(Object.keys(golden.fields));
      for (const field of fields) {
        expect(goldenFields, `${schemaName} lost wire field ${field}`).toContain(field);
      }
    }
  });
});
