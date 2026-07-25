// Adapter for @openai/agents (OpenAI Agents SDK, JS/TS).
//
// Constructs a minimal Agent and runs it in streaming mode, mapping streamed
// text deltas and the final output onto the sidecar protocol.

export async function runOpenAIAgents(req, emit) {
  const sdk = await import("@openai/agents");
  const Agent = sdk.Agent;
  const run = sdk.run;
  if (typeof Agent !== "function" || typeof run !== "function") {
    emit({ type: "error", error: "@openai/agents: Agent/run not found", final: true });
    return;
  }

  const agent = new Agent({
    name: req.name || "AgentMux",
    instructions: req.system_prompt || "You are a helpful assistant.",
    ...(req.model ? { model: req.model } : {}),
  });

  const streamed = await run(agent, req.prompt, { stream: true });

  let sawDelta = false;
  try {
    const textStream = streamed.toTextStream
      ? streamed.toTextStream({ compatibleWithNodeStreams: false })
      : null;
    if (textStream && typeof textStream[Symbol.asyncIterator] === "function") {
      for await (const chunk of textStream) {
        const text = typeof chunk === "string" ? chunk : String(chunk ?? "");
        if (text) {
          sawDelta = true;
          emit({ type: "output", text });
        }
      }
    }
    if (typeof streamed.completed?.then === "function") {
      await streamed.completed;
    }
  } catch (err) {
    emit({ type: "error", error: err?.stack || err?.message || String(err), final: true });
    return;
  }

  const finalOutput =
    typeof streamed.finalOutput === "string"
      ? streamed.finalOutput
      : stringifyOutput(streamed.finalOutput);
  emit({ type: "final", text: finalOutput || (sawDelta ? "" : ""), final: true });
}

function stringifyOutput(output) {
  if (output == null) return "";
  if (typeof output === "string") return output;
  try {
    return JSON.stringify(output);
  } catch {
    return String(output);
  }
}
