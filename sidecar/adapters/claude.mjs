// Adapter for @anthropic-ai/claude-agent-sdk.
//
// Drives one turn via query() in streaming mode and maps the SDK's message
// stream onto the sidecar protocol events consumed by the Go daemon.

export async function runClaudeAgent(req, emit) {
  const sdk = await import("@anthropic-ai/claude-agent-sdk");
  const query = sdk.query;
  if (typeof query !== "function") {
    emit({ type: "error", error: "claude-agent-sdk: query() not found", final: true });
    return;
  }

  const options = {};
  if (req.work_dir) options.cwd = req.work_dir;
  if (req.model) options.model = req.model;
  if (req.system_prompt) options.appendSystemPrompt = req.system_prompt;

  let finalText = "";
  const stream = query({ prompt: req.prompt, options });

  for await (const message of stream) {
    const mapped = mapMessage(message);
    if (!mapped) continue;
    if (mapped.text) finalText = mapped.text;
    emit(mapped);
    if (mapped.final) return;
  }
  emit({ type: "final", text: finalText, final: true });
}

// mapMessage converts a Claude Agent SDK message into a sidecar event, or null
// for control frames we do not surface.
function mapMessage(message) {
  if (!message || typeof message !== "object") return null;
  switch (message.type) {
    case "assistant": {
      const text = extractText(message.message?.content);
      if (!text) return null;
      const usage = extractUsage(message.message);
      return { type: "output", text, usage };
    }
    case "result": {
      const text = typeof message.result === "string" ? message.result : "";
      return { type: "final", text, final: true };
    }
    default:
      return null;
  }
}

function extractText(content) {
  if (!Array.isArray(content)) return "";
  let text = "";
  for (const block of content) {
    if (block && block.type === "text" && typeof block.text === "string") {
      text += block.text;
    }
  }
  return text;
}

function extractUsage(message) {
  const u = message?.usage;
  if (!u) return undefined;
  return {
    model: message.model || "",
    input_tokens: num(u.input_tokens),
    output_tokens: num(u.output_tokens),
    cache_read_tokens: num(u.cache_read_input_tokens),
    cache_write_tokens: num(u.cache_creation_input_tokens),
  };
}

function num(v) {
  return typeof v === "number" ? v : 0;
}
