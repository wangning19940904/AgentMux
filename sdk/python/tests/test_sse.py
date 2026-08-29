from agentmux_sdk._sse import iter_sse_payloads, parse_sse_block


def test_keepalive_comments_are_filtered() -> None:
    stream = [
        "event: started\ndata: {\"type\": \"started\", \"invocation_id\": \"inv-1\"}\n\n",
        ": keepalive\n\n",
        "event: output\ndata: {\"type\": \"output\", \"text\": \"partial\"}\n\n",
    ]
    payloads = list(iter_sse_payloads(iter(stream)))
    assert [payload["type"] for payload in payloads] == ["started", "output"]


def test_chunks_split_mid_event_are_reassembled() -> None:
    raw = 'event: output\ndata: {"type": "output", "text": "hello world"}\n\n'
    chunks = [raw[:10], raw[10:25], raw[25:]]
    payloads = list(iter_sse_payloads(iter(chunks)))
    assert payloads == [{"type": "output", "text": "hello world"}]


def test_crlf_separators() -> None:
    stream = ['data: {"type": "completed"}\r\n\r\ndata: {"type": "error"}\r\n\r\n']
    payloads = list(iter_sse_payloads(iter(stream)))
    assert [payload["type"] for payload in payloads] == ["completed", "error"]


def test_multiline_data_blocks_are_joined() -> None:
    block = 'data: {"type":\ndata: "output"}'
    assert parse_sse_block(block) == {"type": "output"}


def test_comment_only_block_yields_nothing() -> None:
    assert parse_sse_block(": keepalive") is None


def test_trailing_block_without_separator_is_flushed() -> None:
    stream = ['data: {"type": "completed"}']
    payloads = list(iter_sse_payloads(iter(stream)))
    assert payloads == [{"type": "completed"}]
