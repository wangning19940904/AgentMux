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

# Python model attribute -> golden wire field, per schema file.
MODEL_FIELD_MAP: dict[str, tuple[type, dict[str, str]]] = {
    "invocation_result.json": (
        models.InvocationResult,
        {
            "id": "id",
            "conversation_id": "conversation_id",
            "answer": "answer",
            "duration_ms": "duration_ms",
            "agent_id": "agent_id",
            "project": "project",
            "session_id": "session_id",
            "usage": "usage",
        },
    ),
    "invocation_stream_event.json": (
        models.InvocationEvent,
        {
            "type": "type",
            "invocation_id": "invocation_id",
            "conversation_id": "conversation_id",
            "session_id": "session_id",
            "event_id": "event_id",
            "turn_id": "turn_id",
            "item_id": "item_id",
            "text": "text",
            "status": "status",
            "final": "final",
            "duration_ms": "duration_ms",
            "tool_name": "tool_name",
            "tool_call_id": "tool_call_id",
            "tool_input": "tool_input",
            "tool_result": "tool_result",
            "interaction": "interaction",
            "usage": "usage",
            "metadata": "metadata",
            "error": "error",
            "result": "result",
        },
    ),
    "agent_instance.json": (
        models.AgentInstance,
        {
            "id": "id",
            "name": "name",
            "runtime_id": "runtime_id",
            "enabled": "enabled",
            "work_dir": "work_dir",
            "system_prompt": "system_prompt",
            "provider_tool": "provider_tool",
            "default_model": "default_model",
            "default_reasoning_effort": "default_reasoning_effort",
            "default_service_tier": "default_service_tier",
            "default_approval_mode": "default_approval_mode",
            "source": "source",
            "owner_tenant_id": "owner_tenant_id",
            "owner_tenant_name": "owner_tenant_name",
            "visibility": "visibility",
        },
    ),
    "channel.json": (
        models.Channel,
        {
            "id": "id",
            "name": "name",
            "type": "type",
            "enabled": "enabled",
            "agent_id": "agent_id",
            "config": "config",
            "owner_tenant_id": "owner_tenant_id",
            "owner_tenant_name": "owner_tenant_name",
            "visibility": "visibility",
        },
    ),
    "trigger.json": (
        models.Trigger,
        {
            "id": "id",
            "name": "name",
            "kind": "kind",
            "enabled": "enabled",
            "agent_id": "agent_id",
            "channel_id": "channel_id",
            "cron_expr": "cron_expr",
            "prompt": "prompt",
            "event": "event",
            "last_status": "last_status",
            "last_error": "last_error",
        },
    ),
    "orchestration.json": (
        models.Orchestration,
        {
            "id": "id",
            "status": "status",
            "max_concurrency": "max_concurrency",
            "name": "name",
            "error": "error",
            "tasks": "tasks",
        },
    ),
    "orchestration_task.json": (
        models.OrchestrationTask,
        {
            "id": "id",
            "input": "input",
            "agent_id": "agent_id",
            "project": "project",
            "depends_on": "depends_on",
            "status": "status",
            "output": "output",
            "error": "error",
            "invocation_id": "invocation_id",
            "conversation_id": "conversation_id",
        },
    ),
    "tenant.json": (
        models.Tenant,
        {
            "id": "id",
            "name": "name",
            "status": "status",
            "kind": "kind",
            "note": "note",
        },
    ),
}


def test_model_fields_exist_in_goldens() -> None:
    for schema_name in sorted(MODEL_FIELD_MAP):
        model, field_map = MODEL_FIELD_MAP[schema_name]
        golden = json.loads((SCHEMAS_DIR / schema_name).read_text())
        golden_fields = set(golden["fields"])
        model_attrs = set(model.__dataclass_fields__)
        for attribute, wire_name in field_map.items():
            assert attribute in model_attrs, f"{model.__name__} lost attribute {attribute}"
            assert wire_name in golden_fields, (
                f"{model.__name__}.{attribute} maps to {wire_name!r} which no longer exists in "
                f"{schema_name}; the contract drifted"
            )
