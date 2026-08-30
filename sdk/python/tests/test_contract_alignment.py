"""Drift detection between the Python models and contract/schemas goldens.

Runs only inside the AgentMux monorepo (the goldens are not shipped in the
wheel). Every wire field a Python model exposes must exist in the golden
schema generated from the Go types.
"""

import json
from pathlib import Path

import pytest

from agentmux_sdk import models

SCHEMAS_DIR = Path(__file__).resolve().parents[3] / "contract" / "schemas"

pytestmark = pytest.mark.skipif(
    not SCHEMAS_DIR.is_dir(), reason="contract goldens only exist in the monorepo"
)

def identity_fields(*names: str) -> dict[str, str]:
    return {name: name for name in names}


# Python model attribute -> golden wire field, per schema file. The mapped wire
# fields must equal the golden set exactly, so additions are caught in both
# directions rather than silently becoming untyped raw data.
MODEL_FIELD_MAP: dict[str, tuple[type, dict[str, str]]] = {
    "invocation_request.json": (models.InvocationRequest, identity_fields(
        "agent_id", "conversation_id", "input", "attachments", "output_schema",
    )),
    "invocation_result.json": (models.InvocationResult, identity_fields(
        "id", "agent_id", "conversation_id", "session_id", "answer", "duration_ms", "usage",
    )),
    "invocation_stream_event.json": (models.InvocationEvent, identity_fields(
        "type", "invocation_id", "conversation_id", "session_id", "event_id", "turn_id",
        "item_id", "text", "status", "final", "duration_ms", "tool_name", "tool_call_id",
        "tool_input", "tool_result", "interaction", "usage", "metadata", "error", "result",
    )),
    "agent_instance.json": (models.AgentInstance, identity_fields(
        "id", "name", "runtime_id", "desktop_thread_id", "work_dir", "workspace_mode",
        "worktree_base_ref", "session_backend", "system_prompt", "provider_tool", "provider_id",
        "provider_name", "default_model", "default_reasoning_effort", "default_service_tier",
        "default_approval_mode", "memory_scope", "env", "channel_bindings", "schedules",
        "mcp_servers", "skills", "clis", "enabled", "source", "owner_tenant_id",
        "owner_tenant_name", "visibility", "created_at", "updated_at",
    )),
    "channel.json": (models.Channel, identity_fields(
        "id", "name", "type", "agent_id", "config", "enabled", "owner_tenant_id",
        "owner_tenant_name", "visibility", "created_at", "updated_at",
    )),
    "trigger.json": (models.Trigger, identity_fields(
        "id", "name", "kind", "agent_id", "channel_id", "chat_id", "cron_expr", "prompt",
        "event", "action_type", "action_target", "token", "session_mode", "enabled", "last_run",
        "last_status", "last_error", "owner_tenant_id", "created_at", "updated_at",
    )),
    "orchestration.json": (models.Orchestration, identity_fields(
        "id", "name", "status", "max_concurrency", "error", "tasks", "owner_tenant_id",
        "created_at", "started_at", "finished_at", "updated_at",
    )),
    "orchestration_task.json": (models.OrchestrationTask, identity_fields(
        "id", "orchestration_id", "agent_id", "input", "depends_on", "status", "output",
        "error", "invocation_id", "conversation_id", "created_at", "started_at", "finished_at",
        "updated_at",
    )),
    "tenant.json": (models.Tenant, identity_fields(
        "id", "name", "kind", "status", "note", "created_at", "updated_at",
    )),
    "turn_usage.json": (models.TurnUsage, {
        "model": "Model", "request_id": "RequestID", "requested_model": "RequestedModel",
        "resolved_model": "ResolvedModel", "input_tokens": "InputTokens",
        "output_tokens": "OutputTokens", "cache_read_tokens": "CacheReadTokens",
        "cache_write_tokens": "CacheWriteTokens", "reasoning_tokens": "ReasoningTokens",
        "total_tokens": "TotalTokens", "cumulative": "Cumulative", "attempt": "Attempt",
        "ttft_ms": "TTFTMs", "duration_ms": "DurationMs",
    }),
}


def test_model_fields_exist_in_goldens() -> None:
    for schema_name in sorted(MODEL_FIELD_MAP):
        model, field_map = MODEL_FIELD_MAP[schema_name]
        golden = json.loads((SCHEMAS_DIR / schema_name).read_text())
        golden_fields = set(golden["fields"])
        model_attrs = set(model.__dataclass_fields__)
        assert set(field_map.values()) == golden_fields, f"{schema_name} wire fields drifted"
        for attribute, wire_name in field_map.items():
            assert attribute in model_attrs, f"{model.__name__} lost attribute {attribute}"
            assert wire_name in golden_fields, (
                f"{model.__name__}.{attribute} maps to {wire_name!r} which no longer exists in "
                f"{schema_name}; the contract drifted"
            )
