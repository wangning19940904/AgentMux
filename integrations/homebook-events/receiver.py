"""Standalone durable FastAPI inbox example; does not mutate Homebook tables."""

from __future__ import annotations

import asyncio
import contextlib
import json
import logging
import os
import sqlite3
from contextlib import asynccontextmanager
from pathlib import Path

from fastapi import FastAPI, HTTPException, Request, Response
from agentmux_sdk import verify_event_signature

DB_PATH = Path(os.environ.get("EVENT_INBOX_PATH", "event-inbox.sqlite3"))
LOG = logging.getLogger("homebook.events")
MAX_BODY = 4 * 1024 * 1024


def connect() -> sqlite3.Connection:
    connection = sqlite3.connect(DB_PATH, timeout=2)
    connection.execute("PRAGMA journal_mode=WAL")
    return connection


def initialize() -> None:
    with contextlib.closing(connect()) as connection, connection:
        connection.execute(
            "CREATE TABLE IF NOT EXISTS inbox (event_id TEXT PRIMARY KEY, payload TEXT NOT NULL, processed INTEGER NOT NULL DEFAULT 0)"
        )


def accept(event_id: str, body: bytes) -> None:
    with contextlib.closing(connect()) as connection, connection:
        connection.execute(
            "INSERT OR IGNORE INTO inbox(event_id,payload) VALUES(?,?)",
            (event_id, body.decode("utf-8")),
        )


def process_one() -> bool:
    with contextlib.closing(connect()) as connection, connection:
        connection.execute("BEGIN IMMEDIATE")
        row = connection.execute(
            "SELECT event_id,payload FROM inbox WHERE processed=0 ORDER BY rowid LIMIT 1"
        ).fetchone()
        if not row:
            return False
        event = json.loads(row[1])
        # Replace this with an idempotent business transaction/reconciliation.
        # This example records receipt only; it does not update Homebook tables.
        LOG.info("event received id=%s type=%s", row[0], event["type"])
        connection.execute("UPDATE inbox SET processed=1 WHERE event_id=?", (row[0],))
        return True


async def consume() -> None:
    while True:
        try:
            if not await asyncio.to_thread(process_one):
                await asyncio.sleep(0.5)
        except sqlite3.Error:
            LOG.exception("inbox unavailable")
            await asyncio.sleep(1)


@asynccontextmanager
async def lifespan(_: FastAPI):
    if not os.environ.get("AGENTMUX_EVENT_SECRET"):
        raise RuntimeError("AGENTMUX_EVENT_SECRET is required")
    await asyncio.to_thread(initialize)
    worker = asyncio.create_task(consume())
    try:
        yield
    finally:
        worker.cancel()
        with contextlib.suppress(asyncio.CancelledError):
            await worker


app = FastAPI(lifespan=lifespan)


@app.post("/api/v1/integrations/agentmux/events")
async def receive(request: Request) -> Response:
    body = bytearray()
    async for chunk in request.stream():
        body.extend(chunk)
        if len(body) > MAX_BODY:
            raise HTTPException(413, "event too large")
    payload = bytes(body)
    secret = os.environ.get("AGENTMUX_EVENT_SECRET", "")
    if not verify_event_signature(
        secret,
        request.headers.get("X-AgentMux-Timestamp", ""),
        request.headers.get("X-AgentMux-Delivery-ID", ""),
        request.headers.get("X-AgentMux-Signature", ""),
        payload,
    ):
        raise HTTPException(401, "invalid event signature")
    try:
        event = json.loads(payload)
        if (
            not isinstance(event, dict)
            or not isinstance(event.get("id"), str)
            or not event["id"]
            or not isinstance(event.get("type"), str)
            or event.get("schema_version") != "1"
        ):
            raise ValueError("invalid envelope")
        if event["id"] != request.headers.get("X-AgentMux-Event-ID"):
            raise ValueError("event ID mismatch")
    except (ValueError, UnicodeDecodeError) as exc:
        raise HTTPException(400, "invalid event") from exc
    try:
        await asyncio.to_thread(accept, event["id"], payload)
    except sqlite3.Error as exc:
        raise HTTPException(503, "inbox unavailable; retry later") from exc
    return Response(status_code=202)
