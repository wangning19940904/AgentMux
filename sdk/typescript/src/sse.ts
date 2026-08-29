/**
 * Minimal Server-Sent Events parsing over a ReadableStream.
 *
 * The AgentMux invocation stream emits `event: <type>\ndata: <json>\n\n`
 * blocks plus `: keepalive` comment lines every 15 seconds; comment lines
 * are ignored per the SSE specification.
 */

export function parseSSEBlock(block: string): Record<string, unknown> | null {
  const dataLines = block
    .split(/\r?\n/)
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice(5).trimStart());
  if (dataLines.length === 0) return null;
  try {
    const payload: unknown = JSON.parse(dataLines.join("\n"));
    return payload !== null && typeof payload === "object" && !Array.isArray(payload)
      ? (payload as Record<string, unknown>)
      : null;
  } catch {
    return null;
  }
}

/** Split buffered text into complete SSE blocks, returning the remainder. */
export function splitSSEBuffer(buffer: string): { blocks: string[]; rest: string } {
  const blocks = buffer.split(/\r?\n\r?\n/);
  const rest = blocks.pop() ?? "";
  return { blocks, rest };
}

export async function* iterSSEPayloads(
  body: ReadableStream<Uint8Array>,
): AsyncGenerator<Record<string, unknown>> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      buffer += decoder.decode(value, { stream: !done });
      const { blocks, rest } = splitSSEBuffer(buffer);
      buffer = rest;
      for (const block of blocks) {
        const payload = parseSSEBlock(block);
        if (payload !== null) yield payload;
      }
      if (done) break;
    }
    if (buffer.trim()) {
      const payload = parseSSEBlock(buffer);
      if (payload !== null) yield payload;
    }
  } finally {
    reader.releaseLock();
  }
}
