import asyncio
import hashlib
import hmac
import json
from dataclasses import fields
from pathlib import Path

import httpx

from agentmux_sdk import (
    AgentMuxClient,
    AsyncAgentMuxClient,
    EventDelivery,
    EventDeliveryAttempt,
    EventIngestionHealth,
    EventSource,
    EventSubscription,
    RelayEvent,
    verify_event_signature,
)


def test_event_models_match_go_contract():
    schemas = Path(__file__).resolve().parents[3] / "contract" / "schemas"
    for name, model in {
        "relay_event": RelayEvent,
        "event_subscription": EventSubscription,
        "event_delivery": EventDelivery,
        "event_delivery_attempt": EventDeliveryAttempt,
        "event_source": EventSource,
        "event_ingestion_health": EventIngestionHealth,
    }.items():
        assert {f.name for f in fields(model)} == set(
            json.loads((schemas / f"{name}.json").read_text())["fields"]
        )


def test_signature_and_window():
    body = b'{"id":"test"}'
    stamp = "2000000000"
    sig = (
        "v1="
        + hmac.new(
            b"secret", stamp.encode() + b".delivery." + body, hashlib.sha256
        ).hexdigest()
    )
    assert verify_event_signature(
        "secret", stamp, "delivery", sig, body, now=2000000000
    )
    assert not verify_event_signature(
        "secret", stamp, "delivery", sig, body + b" ", now=2000000000
    )
    assert not verify_event_signature(
        "secret", stamp, "other", sig, body, now=2000000000
    )
    assert not verify_event_signature(
        "secret", stamp, "delivery", sig, body, now=2000000301
    )
    assert not verify_event_signature("secret", "broken", "delivery", sig, body)


def test_event_resource_sync_and_async():
    calls = []

    def handler(request):
        calls.append((request.method, request.url.path))
        if (
            request.url.path.endswith("event-subscriptions")
            and request.method == "POST"
        ):
            return httpx.Response(
                200,
                json={
                    "subscription": {"id": "sub", **json.loads(request.content)},
                    "signing_secret": "once",
                },
            )
        if request.url.path.endswith("rotate-secret"):
            return httpx.Response(200, json={"signing_secret": "rotated"})
        if request.url.path.endswith("event-deliveries"):
            return httpx.Response(
                200, json={"items": [], "limit": 25, "offset": 0, "has_more": False}
            )
        return httpx.Response(200, json=[] if request.method == "GET" else {"ok": True})

    payload = {
        "key": "homebook",
        "name": "Homebook",
        "source": "feishu",
        "source_refs": ["channel:ch"],
        "event_types": ["changed"],
        "callback_url": "http://127.0.0.1:8000/events",
    }
    with AgentMuxClient(
        transport=httpx.MockTransport(handler), token="tenant"
    ) as client:
        assert client.events.sources() == []
        result = client.events.upsert(payload)
        assert result.subscription.id == "sub" and result.signing_secret == "once"
        assert "once" not in repr(result)
        assert client.events.list() == []
        client.events.test("sub")
        assert client.events.rotate_secret("sub") == "rotated"
        assert not client.events.deliveries(limit=25).has_more
        client.events.retry("delivery")
        client.events.delete("sub")

    async def run():
        async with AsyncAgentMuxClient(
            transport=httpx.MockTransport(handler)
        ) as client:
            assert await client.events.sources() == []
            assert (await client.events.upsert(payload)).signing_secret == "once"
            assert await client.events.list() == []
            await client.events.test("sub")
            assert await client.events.rotate_secret("sub") == "rotated"
            assert not (await client.events.deliveries(limit=25)).has_more
            await client.events.retry("delivery")
            await client.events.delete("sub")

    asyncio.run(run())
    assert len(calls) == 16
