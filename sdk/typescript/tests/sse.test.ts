import { describe, expect, it } from "vitest";

import { iterSSEPayloads, parseSSEBlock } from "../src/sse.js";

function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
}

async function collect(stream: ReadableStream<Uint8Array>) {
  const payloads: Record<string, unknown>[] = [];
  for await (const payload of iterSSEPayloads(stream)) payloads.push(payload);
  return payloads;
}

describe("SSE parsing", () => {
  it("filters keepalive comments", async () => {
    const payloads = await collect(
      streamOf([
        'event: started\ndata: {"type":"started"}\n\n',
        ": keepalive\n\n",
        'event: output\ndata: {"type":"output","text":"hi"}\n\n',
      ]),
    );
    expect(payloads.map((payload) => payload.type)).toEqual(["started", "output"]);
  });

  it("reassembles events split across chunks", async () => {
    const raw = 'event: output\ndata: {"type":"output","text":"hello world"}\n\n';
    const payloads = await collect(streamOf([raw.slice(0, 12), raw.slice(12, 30), raw.slice(30)]));
    expect(payloads).toEqual([{ type: "output", text: "hello world" }]);
  });

  it("handles CRLF separators and trailing blocks", async () => {
    const payloads = await collect(
      streamOf(['data: {"type":"completed"}\r\n\r\ndata: {"type":"error"}']),
    );
    expect(payloads.map((payload) => payload.type)).toEqual(["completed", "error"]);
  });

  it("joins multi-line data fields", () => {
    expect(parseSSEBlock('data: {"type":\ndata: "output"}')).toEqual({ type: "output" });
  });

  it("returns null for comment-only blocks", () => {
    expect(parseSSEBlock(": keepalive")).toBeNull();
  });
});
