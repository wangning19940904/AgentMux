import hashlib
import hmac
import importlib.util
import json
import sqlite3
import time
from pathlib import Path

from fastapi.testclient import TestClient

spec = importlib.util.spec_from_file_location(
    "event_receiver_example", Path(__file__).with_name("receiver.py")
)
receiver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(receiver)


def headers(body, delivery="delivery-1", timestamp=None):
    stamp = str(int(time.time()) if timestamp is None else timestamp)
    signature = (
        "v1="
        + hmac.new(
            b"secret", f"{stamp}.{delivery}.".encode() + body, hashlib.sha256
        ).hexdigest()
    )
    return {
        "Content-Type": "application/json",
        "X-AgentMux-Event-ID": "event-1",
        "X-AgentMux-Delivery-ID": delivery,
        "X-AgentMux-Timestamp": stamp,
        "X-AgentMux-Signature": signature,
    }


def test_durable_inbox_duplicate_and_signature(monkeypatch, tmp_path):
    monkeypatch.setenv("AGENTMUX_EVENT_SECRET", "secret")
    monkeypatch.setattr(receiver, "DB_PATH", tmp_path / "inbox.sqlite3")
    body = json.dumps(
        {
            "schema_version": "1",
            "id": "event-1",
            "type": "agentmux.subscription.test",
            "data": {"test": True},
        }
    ).encode()
    with TestClient(receiver.app) as client:
        endpoint = "/api/v1/integrations/agentmux/events"
        assert (
            client.post(endpoint, content=body, headers=headers(body)).status_code
            == 202
        )
        assert (
            client.post(
                endpoint, content=body, headers=headers(body, "delivery-2")
            ).status_code
            == 202
        )
        with sqlite3.connect(receiver.DB_PATH) as db:
            assert db.execute("SELECT COUNT(*) FROM inbox").fetchone()[0] == 1
        assert (
            client.post(
                endpoint, content=body + b" ", headers=headers(body)
            ).status_code
            == 401
        )
        assert (
            client.post(
                endpoint,
                content=body,
                headers=headers(body, timestamp=int(time.time()) - 301),
            ).status_code
            == 401
        )

        def unavailable(*_args):
            raise sqlite3.OperationalError("offline")

        monkeypatch.setattr(receiver, "accept", unavailable)
        assert (
            client.post(endpoint, content=body, headers=headers(body)).status_code
            == 503
        )
