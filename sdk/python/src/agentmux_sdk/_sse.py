"""Minimal Server-Sent Events parsing shared by the sync and async clients.

The AgentMux invocation stream emits ``event: <type>\\ndata: <json>\\n\\n``
blocks plus ``: keepalive`` comment lines every 15 seconds. Comment lines
(starting with ``:``) must be ignored per the SSE specification.
"""

from __future__ import annotations

import json
from collections.abc import AsyncIterator, Iterator
from typing import Any


class SSEBlockAssembler:
    """Feed decoded text chunks, get complete event blocks back."""

    def __init__(self) -> None:
        self._buffer = ""

    def feed(self, chunk: str) -> list[str]:
        self._buffer += chunk
        blocks: list[str] = []
        while True:
            for separator in ("\r\n\r\n", "\n\n"):
                index = self._buffer.find(separator)
                if index >= 0:
                    blocks.append(self._buffer[:index])
                    self._buffer = self._buffer[index + len(separator):]
                    break
            else:
                return blocks

    def flush(self) -> str:
        remaining, self._buffer = self._buffer, ""
        return remaining


def parse_sse_block(block: str) -> dict[str, Any] | None:
    """Extract the JSON payload of one SSE block; None for comments/empties."""
    data_lines = [
        line[5:].lstrip()
        for line in block.splitlines()
        if line.startswith("data:")
    ]
    if not data_lines:
        return None
    try:
        payload = json.loads("\n".join(data_lines))
    except ValueError:
        return None
    return payload if isinstance(payload, dict) else None


def iter_sse_payloads(chunks: Iterator[str]) -> Iterator[dict[str, Any]]:
    assembler = SSEBlockAssembler()
    for chunk in chunks:
        for block in assembler.feed(chunk):
            payload = parse_sse_block(block)
            if payload is not None:
                yield payload
    tail = assembler.flush()
    if tail.strip():
        payload = parse_sse_block(tail)
        if payload is not None:
            yield payload


async def aiter_sse_payloads(chunks: AsyncIterator[str]) -> AsyncIterator[dict[str, Any]]:
    assembler = SSEBlockAssembler()
    async for chunk in chunks:
        for block in assembler.feed(chunk):
            payload = parse_sse_block(block)
            if payload is not None:
                yield payload
    tail = assembler.flush()
    if tail.strip():
        payload = parse_sse_block(tail)
        if payload is not None:
            yield payload
