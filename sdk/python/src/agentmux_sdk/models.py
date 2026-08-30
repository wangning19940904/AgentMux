"""Wire models aligned with contract/schemas (the golden JSON schemas
generated from the AgentMux Go types).

All ``from_dict`` constructors tolerate unknown keys so an old SDK keeps
working against a newer, backward-compatible server (contract minor bumps).
The full payload is preserved on ``raw`` for forward-compatible access.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any

# Contract range this SDK speaks; servers outside it report `incompatible`.
SUPPORTED_CONTRACT_MAJOR = 2


class HealthState(StrEnum):
    """Unified 5-state health machine (see contract/CONTRACT.md)."""

    READY = "ready"
    UNAUTHORIZED = "unauthorized"
    INCOMPATIBLE = "incompatible"
    UNREACHABLE = "unreachable"
    MISSING = "missing"


_VERSION_PATTERN = re.compile(r"v?(\d+)\.(\d+)(?:\.(\d+))?")


def version_key(version: str | None) -> tuple[int, int, int] | None:
    """Parse ``v1.2.3`` / ``1.2.3`` / ``1.2`` into a comparable tuple."""
    match = _VERSION_PATTERN.search(version or "")
    if match is None:
        return None
    major, minor, patch = match.groups()
    return int(major), int(minor), int(patch or 0)


def contract_major(contract_version: str | None) -> int | None:
    key = version_key(contract_version)
    return key[0] if key else None


@dataclass(frozen=True)
class ModuleState:
    configured: bool
    runtime_active: bool
    enforced: bool

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ModuleState":
        return cls(
            configured=bool(data.get("configured")),
            runtime_active=bool(data.get("runtime_active")),
            enforced=bool(data.get("enforced")),
        )


@dataclass(frozen=True)
class Capabilities:
    """Response of GET /api/v1/capabilities."""

    ok: bool
    product: str
    version: str
    contract_version: str
    features: tuple[str, ...]
    modules: dict[str, ModuleState]
    agents: dict[str, Any]
    channels: dict[str, Any]
    bridge_enabled: bool
    # Tenancy (contract 1.1): "admin" when the credential sees the whole
    # instance, "tenant" when it is confined to one application. Older servers
    # omit it, in which case there is no tenancy and the scope is admin.
    scope: str = "admin"
    tenant: str | None = None
    tenant_id: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Capabilities":
        auth = data.get("auth") or {}
        return cls(
            ok=bool(data.get("ok")),
            product=str(data.get("product") or ""),
            version=str(data.get("version") or ""),
            contract_version=str(data.get("contract_version") or ""),
            features=tuple(data.get("features") or ()),
            modules={
                str(name): ModuleState.from_dict(state)
                for name, state in (data.get("modules") or {}).items()
                if isinstance(state, dict)
            },
            agents=dict(data.get("agents") or {}),
            channels=dict(data.get("channels") or {}),
            bridge_enabled=bool(auth.get("bridge_enabled")),
            scope=str(auth.get("scope") or "admin"),
            tenant=auth.get("tenant"),
            tenant_id=auth.get("tenant_id"),
            raw=data,
        )

    def supports(self, feature: str) -> bool:
        return feature in self.features

    @property
    def tenant_scoped(self) -> bool:
        """True when this credential only sees one application's resources."""
        return self.scope == "tenant"


@dataclass(frozen=True)
class HealthReport:
    """Aggregated health as reported by ``client.health()``."""

    state: HealthState
    message: str
    version: str | None = None
    contract_version: str | None = None
    console_url: str | None = None
    capabilities: Capabilities | None = None

    @property
    def ready(self) -> bool:
        return self.state is HealthState.READY


@dataclass(frozen=True)
class Attachment:
    """Invocation attachment. ``kind`` is ``image`` or ``file``."""

    kind: str
    name: str | None = None
    mime_type: str | None = None
    path: str | None = None
    url: str | None = None

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {"kind": self.kind}
        if self.name:
            payload["name"] = self.name
        if self.mime_type:
            payload["mime_type"] = self.mime_type
        if self.path:
            payload["path"] = self.path
        if self.url:
            payload["url"] = self.url
        return payload


@dataclass(frozen=True)
class InvocationRequest:
    """Wire request for POST /api/v1/invocations."""

    agent_id: str
    input: str
    conversation_id: str | None = None
    attachments: tuple[Attachment, ...] = ()
    output_schema: dict[str, Any] | None = None


@dataclass(frozen=True)
class TurnUsage:
    """Per-turn token usage using the contract's legacy wire names."""

    model: str = ""
    request_id: str = ""
    requested_model: str = ""
    resolved_model: str = ""
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0
    reasoning_tokens: int = 0
    total_tokens: int = 0
    cumulative: bool = False
    attempt: int = 0
    ttft_ms: int = 0
    duration_ms: int = 0

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "TurnUsage":
        return cls(
            model=str(data.get("Model") or ""),
            request_id=str(data.get("RequestID") or ""),
            requested_model=str(data.get("RequestedModel") or ""),
            resolved_model=str(data.get("ResolvedModel") or ""),
            input_tokens=int(data.get("InputTokens") or 0),
            output_tokens=int(data.get("OutputTokens") or 0),
            cache_read_tokens=int(data.get("CacheReadTokens") or 0),
            cache_write_tokens=int(data.get("CacheWriteTokens") or 0),
            reasoning_tokens=int(data.get("ReasoningTokens") or 0),
            total_tokens=int(data.get("TotalTokens") or 0),
            cumulative=bool(data.get("Cumulative")),
            attempt=int(data.get("Attempt") or 0),
            ttft_ms=int(data.get("TTFTMs") or 0),
            duration_ms=int(data.get("DurationMs") or 0),
        )


@dataclass(frozen=True)
class InvocationResult:
    """Response of POST /api/v1/invocations (also completed.result in SSE)."""

    id: str
    conversation_id: str
    answer: str
    duration_ms: int
    agent_id: str | None = None
    session_id: str | None = None
    usage: TurnUsage | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "InvocationResult":
        return cls(
            id=str(data.get("id") or ""),
            conversation_id=str(data.get("conversation_id") or ""),
            answer=str(data.get("answer") or ""),
            duration_ms=int(data.get("duration_ms") or 0),
            agent_id=data.get("agent_id"),
            session_id=data.get("session_id"),
            usage=TurnUsage.from_dict(data["usage"]) if isinstance(data.get("usage"), dict) else None,
            raw=data,
        )


@dataclass(frozen=True)
class InvocationEvent:
    """One SSE event from POST /api/v1/invocations/stream.

    The ``text`` of ``output``/``thinking`` events is a **full snapshot**:
    replace what you previously displayed instead of appending.
    """

    type: str
    invocation_id: str | None = None
    conversation_id: str | None = None
    session_id: str | None = None
    event_id: str | None = None
    turn_id: str | None = None
    item_id: str | None = None
    text: str = ""
    status: str | None = None
    final: bool = False
    duration_ms: int = 0
    tool_name: str | None = None
    tool_call_id: str | None = None
    tool_input: str | None = None
    tool_result: str | None = None
    interaction: dict[str, Any] | None = None
    usage: TurnUsage | None = None
    metadata: dict[str, str] | None = None
    error: str | None = None
    result: InvocationResult | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "InvocationEvent":
        result = data.get("result")
        return cls(
            type=str(data.get("type") or "event"),
            invocation_id=data.get("invocation_id"),
            conversation_id=data.get("conversation_id"),
            session_id=data.get("session_id"),
            event_id=data.get("event_id"),
            turn_id=data.get("turn_id"),
            item_id=data.get("item_id"),
            text=str(data.get("text") or ""),
            status=data.get("status"),
            final=bool(data.get("final")),
            duration_ms=int(data.get("duration_ms") or 0),
            tool_name=data.get("tool_name"),
            tool_call_id=data.get("tool_call_id"),
            tool_input=data.get("tool_input"),
            tool_result=data.get("tool_result"),
            interaction=data.get("interaction"),
            usage=TurnUsage.from_dict(data["usage"]) if isinstance(data.get("usage"), dict) else None,
            metadata=data.get("metadata"),
            error=data.get("error"),
            result=InvocationResult.from_dict(result) if isinstance(result, dict) else None,
            raw=data,
        )


@dataclass(frozen=True)
class AgentInstance:
    """Console-managed Agent (contract/schemas/agent_instance.json)."""

    id: str
    name: str
    runtime_id: str
    enabled: bool
    desktop_thread_id: str | None = None
    work_dir: str | None = None
    workspace_mode: str | None = None
    worktree_base_ref: str | None = None
    session_backend: str | None = None
    system_prompt: str | None = None
    provider_tool: str | None = None
    provider_id: str | None = None
    provider_name: str | None = None
    default_model: str | None = None
    default_reasoning_effort: str | None = None
    default_service_tier: str | None = None
    default_approval_mode: str | None = None
    memory_scope: str | None = None
    env: dict[str, str] = field(default_factory=dict)
    channel_bindings: tuple[dict[str, Any], ...] = ()
    schedules: tuple[dict[str, Any], ...] = ()
    mcp_servers: tuple[str, ...] = ()
    skills: tuple[str, ...] = ()
    clis: tuple[str, ...] = ()
    source: str | None = None
    owner_tenant_id: str | None = None
    owner_tenant_name: str | None = None
    visibility: str | None = None
    created_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "AgentInstance":
        return cls(
            id=str(data.get("id") or ""),
            name=str(data.get("name") or ""),
            runtime_id=str(data.get("runtime_id") or ""),
            enabled=bool(data.get("enabled")),
            desktop_thread_id=data.get("desktop_thread_id"),
            work_dir=data.get("work_dir"),
            workspace_mode=data.get("workspace_mode"),
            worktree_base_ref=data.get("worktree_base_ref"),
            session_backend=data.get("session_backend"),
            system_prompt=data.get("system_prompt"),
            provider_tool=data.get("provider_tool"),
            provider_id=data.get("provider_id"),
            provider_name=data.get("provider_name"),
            default_model=data.get("default_model"),
            default_reasoning_effort=data.get("default_reasoning_effort"),
            default_service_tier=data.get("default_service_tier"),
            default_approval_mode=data.get("default_approval_mode"),
            memory_scope=data.get("memory_scope"),
            env=dict(data.get("env") or {}),
            channel_bindings=tuple(data.get("channel_bindings") or ()),
            schedules=tuple(data.get("schedules") or ()),
            mcp_servers=tuple(data.get("mcp_servers") or ()),
            skills=tuple(data.get("skills") or ()),
            clis=tuple(data.get("clis") or ()),
            source=data.get("source"),
            owner_tenant_id=data.get("owner_tenant_id"),
            owner_tenant_name=data.get("owner_tenant_name"),
            visibility=data.get("visibility"),
            created_at=data.get("created_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    def to_payload(self) -> dict[str, Any]:
        payload = dict(self.raw)
        payload.update(
            {
                "id": self.id,
                "name": self.name,
                "runtime_id": self.runtime_id,
                "enabled": self.enabled,
            }
        )
        for key, value in (
            ("work_dir", self.work_dir),
            ("desktop_thread_id", self.desktop_thread_id),
            ("workspace_mode", self.workspace_mode),
            ("worktree_base_ref", self.worktree_base_ref),
            ("session_backend", self.session_backend),
            ("system_prompt", self.system_prompt),
            ("provider_tool", self.provider_tool),
            ("provider_id", self.provider_id),
            ("default_model", self.default_model),
            ("default_reasoning_effort", self.default_reasoning_effort),
            ("default_service_tier", self.default_service_tier),
            ("default_approval_mode", self.default_approval_mode),
            ("memory_scope", self.memory_scope),
            ("source", self.source),
        ):
            if value is not None:
                payload[key] = value
        for key, value in (
            ("env", self.env),
            ("channel_bindings", list(self.channel_bindings)),
            ("schedules", list(self.schedules)),
            ("mcp_servers", list(self.mcp_servers)),
            ("skills", list(self.skills)),
            ("clis", list(self.clis)),
        ):
            if value:
                payload[key] = value
        # Ownership is assigned by the server from the calling credential, so
        # it is deliberately not echoed back on writes.
        payload.pop("owner_tenant_id", None)
        payload.pop("owner_tenant_name", None)
        return payload


@dataclass(frozen=True)
class Channel:
    """IM channel (contract/schemas/channel.json)."""

    id: str
    name: str
    type: str
    enabled: bool
    agent_id: str | None = None
    config: dict[str, str] = field(default_factory=dict)
    owner_tenant_id: str | None = None
    owner_tenant_name: str | None = None
    visibility: str | None = None
    created_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Channel":
        return cls(
            id=str(data.get("id") or ""),
            name=str(data.get("name") or ""),
            type=str(data.get("type") or ""),
            enabled=bool(data.get("enabled")),
            agent_id=data.get("agent_id"),
            config=dict(data.get("config") or {}),
            owner_tenant_id=data.get("owner_tenant_id"),
            owner_tenant_name=data.get("owner_tenant_name"),
            visibility=data.get("visibility"),
            created_at=data.get("created_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    def to_payload(self) -> dict[str, Any]:
        payload = dict(self.raw)
        payload.update(
            {"id": self.id, "name": self.name, "type": self.type, "enabled": self.enabled}
        )
        if self.agent_id is not None:
            payload["agent_id"] = self.agent_id
        if self.config:
            payload["config"] = self.config
        payload.pop("owner_tenant_id", None)
        payload.pop("owner_tenant_name", None)
        return payload


@dataclass(frozen=True)
class Trigger:
    """Automation trigger (contract/schemas/trigger.json)."""

    id: str
    name: str
    kind: str
    enabled: bool
    agent_id: str | None = None
    channel_id: str | None = None
    chat_id: str | None = None
    cron_expr: str | None = None
    prompt: str | None = None
    event: str | None = None
    action_type: str | None = None
    action_target: str | None = None
    token: str | None = None
    session_mode: str | None = None
    last_run: str | None = None
    last_status: str | None = None
    last_error: str | None = None
    owner_tenant_id: str | None = None
    created_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Trigger":
        return cls(
            id=str(data.get("id") or ""),
            name=str(data.get("name") or ""),
            kind=str(data.get("kind") or ""),
            enabled=bool(data.get("enabled")),
            agent_id=data.get("agent_id"),
            channel_id=data.get("channel_id"),
            chat_id=data.get("chat_id"),
            cron_expr=data.get("cron_expr"),
            prompt=data.get("prompt"),
            event=data.get("event"),
            action_type=data.get("action_type"),
            action_target=data.get("action_target"),
            token=data.get("token"),
            session_mode=data.get("session_mode"),
            last_run=data.get("last_run"),
            last_status=data.get("last_status"),
            last_error=data.get("last_error"),
            owner_tenant_id=data.get("owner_tenant_id"),
            created_at=data.get("created_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    def to_payload(self) -> dict[str, Any]:
        payload = dict(self.raw)
        payload.update(
            {"id": self.id, "name": self.name, "kind": self.kind, "enabled": self.enabled}
        )
        for key, value in (
            ("agent_id", self.agent_id),
            ("channel_id", self.channel_id),
            ("chat_id", self.chat_id),
            ("cron_expr", self.cron_expr),
            ("prompt", self.prompt),
            ("event", self.event),
            ("action_type", self.action_type),
            ("action_target", self.action_target),
            ("token", self.token),
            ("session_mode", self.session_mode),
        ):
            if value is not None:
                payload[key] = value
        return payload


@dataclass(frozen=True)
class OrchestrationTask:
    """DAG task (contract/schemas/orchestration_task.json)."""

    id: str
    input: str
    agent_id: str
    orchestration_id: str | None = None
    depends_on: tuple[str, ...] = ()
    status: str | None = None
    output: str | None = None
    error: str | None = None
    invocation_id: str | None = None
    conversation_id: str | None = None
    created_at: str | None = None
    started_at: str | None = None
    finished_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "OrchestrationTask":
        return cls(
            id=str(data.get("id") or ""),
            input=str(data.get("input") or ""),
            agent_id=str(data.get("agent_id") or ""),
            orchestration_id=data.get("orchestration_id"),
            depends_on=tuple(data.get("depends_on") or ()),
            status=data.get("status"),
            output=data.get("output"),
            error=data.get("error"),
            invocation_id=data.get("invocation_id"),
            conversation_id=data.get("conversation_id"),
            created_at=data.get("created_at"),
            started_at=data.get("started_at"),
            finished_at=data.get("finished_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    def to_payload(self) -> dict[str, Any]:
        payload: dict[str, Any] = {"id": self.id, "input": self.input}
        if self.agent_id:
            payload["agent_id"] = self.agent_id
        if self.depends_on:
            payload["depends_on"] = list(self.depends_on)
        return payload


@dataclass(frozen=True)
class Orchestration:
    """Multi-agent DAG (contract/schemas/orchestration.json)."""

    id: str
    status: str
    max_concurrency: int
    name: str | None = None
    error: str | None = None
    tasks: tuple[OrchestrationTask, ...] = ()
    owner_tenant_id: str | None = None
    created_at: str | None = None
    started_at: str | None = None
    finished_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Orchestration":
        return cls(
            id=str(data.get("id") or ""),
            status=str(data.get("status") or ""),
            max_concurrency=int(data.get("max_concurrency") or 0),
            name=data.get("name"),
            error=data.get("error"),
            tasks=tuple(
                OrchestrationTask.from_dict(task)
                for task in data.get("tasks") or ()
                if isinstance(task, dict)
            ),
            owner_tenant_id=data.get("owner_tenant_id"),
            created_at=data.get("created_at"),
            started_at=data.get("started_at"),
            finished_at=data.get("finished_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    @property
    def terminal(self) -> bool:
        return self.status in {"succeeded", "failed", "cancelled"}


@dataclass(frozen=True)
class IntegrationSnapshot:
    """Tenant-scoped host application view composed by the SDK.

    This is intentionally an SDK aggregate rather than a new server endpoint:
    every item still comes from the public, versioned AgentMux contract and is
    filtered by the caller's tenant credential on the server.
    """

    capabilities: Capabilities
    identity: "TenancySelf"
    runtimes: tuple[str, ...] = ()
    platforms: tuple[str, ...] = ()
    agents: tuple[AgentInstance, ...] = ()
    channels: tuple[Channel, ...] = ()
    triggers: tuple[Trigger, ...] = ()
    orchestrations: tuple[Orchestration, ...] = ()


@dataclass(frozen=True)
class ConsoleSession:
    """Response of POST /api/v1/console/sessions."""

    enter_url: str
    expires_at: str
    session_ttl_seconds: int
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "ConsoleSession":
        return cls(
            enter_url=str(data.get("enter_url") or ""),
            expires_at=str(data.get("expires_at") or ""),
            session_ttl_seconds=int(data.get("session_ttl_seconds") or 0),
            raw=data,
        )


@dataclass(frozen=True)
class Tenant:
    """A registered host application (contract/schemas/tenant.json)."""

    id: str
    name: str
    status: str
    kind: str | None = None
    note: str | None = None
    created_at: str | None = None
    updated_at: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "Tenant":
        return cls(
            id=str(data.get("id") or ""),
            name=str(data.get("name") or ""),
            status=str(data.get("status") or ""),
            kind=data.get("kind"),
            note=data.get("note"),
            created_at=data.get("created_at"),
            updated_at=data.get("updated_at"),
            raw=data,
        )

    @property
    def active(self) -> bool:
        return self.status == "active"


@dataclass(frozen=True)
class TenantRegistration:
    """Result of self-registering a tenant (POST /api/v1/tenancy/register).

    ``token`` is the application's long-lived credential. It is returned once
    and cannot be recovered, so persist it before discarding this object.
    """

    tenant: Tenant
    token: str
    prefix: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "TenantRegistration":
        tenant = data.get("tenant")
        return cls(
            tenant=Tenant.from_dict(tenant if isinstance(tenant, dict) else {}),
            token=str(data.get("token") or ""),
            prefix=data.get("prefix"),
            raw=data,
        )


@dataclass(frozen=True)
class TenancySelf:
    """Response of GET /api/v1/tenancy/self."""

    admin: bool
    tenant: str | None = None
    tenant_id: str | None = None
    kind: str | None = None
    status: str | None = None
    raw: dict[str, Any] = field(repr=False, default_factory=dict)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> "TenancySelf":
        return cls(
            admin=bool(data.get("admin")),
            tenant=data.get("tenant"),
            tenant_id=data.get("tenant_id"),
            kind=data.get("kind"),
            status=data.get("status"),
            raw=data,
        )
