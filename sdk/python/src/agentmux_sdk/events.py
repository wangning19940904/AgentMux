"""Event relay wire models and raw-body callback verification (contract 2.2)."""

from __future__ import annotations

import hashlib
import hmac
import time
from dataclasses import dataclass, field, fields
from typing import Any, Self


class EventWireModel:
    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Self:
        names = {item.name for item in fields(cls)}  # type: ignore[arg-type]
        return cls(**{key: value for key, value in data.items() if key in names})


@dataclass
class RelayEvent(EventWireModel):
    schema_version: str
    id: str
    source: str
    type: str
    source_ref: str
    occurred_at: str
    received_at: str
    attributes: dict[str, str]
    data: Any
    source_event_id: str | None = None


@dataclass
class EventSubscription(EventWireModel):
    id: str = ""
    key: str = ""
    name: str = ""
    source: str = "agentmux"
    source_refs: list[str] = field(default_factory=list)
    event_types: list[str] = field(default_factory=list)
    callback_url: str = ""
    paused: bool = False
    owner_tenant_id: str | None = None
    filters: dict[str, list[str]] = field(default_factory=dict)
    created_at: str = ""
    updated_at: str = ""
    deleted: bool = False
    pending: int = 0
    dead: int = 0
    last_success_at: str | None = None
    last_error: str | None = None

    def to_payload(self) -> dict[str, Any]:
        keys = (
            "id",
            "key",
            "name",
            "source",
            "source_refs",
            "event_types",
            "filters",
            "callback_url",
            "paused",
            "owner_tenant_id",
        )
        return {key: getattr(self, key) for key in keys}


@dataclass
class EventSubscriptionResult(EventWireModel):
    subscription: EventSubscription
    signing_secret: str | None = field(default=None, repr=False)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Self:
        return cls(
            EventSubscription.from_dict(data["subscription"]),
            data.get("signing_secret"),
        )


@dataclass
class EventIngestionHealth(EventWireModel):
    failures: int = 0
    last_error: str | None = None
    last_failure_at: str | None = None
    last_success_at: str | None = None


@dataclass
class EventSource(EventWireModel):
    ref: str
    name: str
    sources: list[str]
    event_types: list[str]
    connected: bool
    state: str
    platform_verified: bool
    ingestion: EventIngestionHealth

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Self:
        return super().from_dict(
            {
                **data,
                "ingestion": EventIngestionHealth.from_dict(
                    data.get("ingestion") or {}
                ),
            }
        )


@dataclass
class EventDeliveryAttempt(EventWireModel):
    id: str
    delivery_id: str
    started_at: str
    http_status: int = 0
    finished_at: str | None = None
    error: str | None = None


@dataclass
class EventDelivery(EventWireModel):
    id: str
    subscription_id: str
    event_id: str
    status: str
    attempts: int
    next_attempt_at: str
    created_at: str
    updated_at: str
    first_attempt_at: str | None = None
    last_error: str | None = None
    event: RelayEvent | None = None
    history: list[EventDeliveryAttempt] = field(default_factory=list)

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Self:
        payload = dict(data)
        payload["event"] = (
            RelayEvent.from_dict(data["event"]) if data.get("event") else None
        )
        payload["history"] = [
            EventDeliveryAttempt.from_dict(item) for item in data.get("history") or []
        ]
        return super().from_dict(payload)


@dataclass
class EventDeliveryPage(EventWireModel):
    items: list[EventDelivery]
    limit: int
    offset: int
    has_more: bool

    @classmethod
    def from_dict(cls, data: dict[str, Any]) -> Self:
        return cls(
            [EventDelivery.from_dict(item) for item in data["items"]],
            data["limit"],
            data["offset"],
            data["has_more"],
        )


def verify_event_signature(
    secret: str,
    timestamp: str,
    delivery_id: str,
    signature: str,
    body: bytes,
    *,
    now: float | None = None,
) -> bool:
    """Verify before parsing JSON. Store event IDs durably to prevent duplicate work."""
    if (
        not secret
        or not delivery_id
        or not timestamp.isascii()
        or not timestamp.isdecimal()
    ):
        return False
    try:
        age = (time.time() if now is None else now) - int(timestamp)
    except (ValueError, OverflowError):
        return False
    if abs(age) > 300:
        return False
    message = f"{timestamp}.{delivery_id}.".encode() + body
    expected = "v1=" + hmac.new(secret.encode(), message, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected.encode(), signature.encode())
