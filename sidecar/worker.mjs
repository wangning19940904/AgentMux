// AgentMux Node sidecar worker.
//
// The Go daemon spawns this process once and speaks a line-delimited JSON
// protocol over stdio:
//
//   Go -> sidecar (stdin), one JSON object per line:
//     { "id": "<req>", "type": "run", "kind": "claude-agent-sdk",
//       "prompt": "...", "system_prompt": "...", "work_dir": "...",
//       "model": "...", "env": { "ANTHROPIC_API_KEY": "..." } }
//     { "id": "<req>", "type": "ping" }
//
//   sidecar -> Go (stdout), one JSON object per line:
//     { "id": "<req>", "type": "output"|"tool_use"|"final"|"error",
//       "text": "...", "final": true, "error": "...", "usage": {...} }
//     { "type": "ready" }              // emitted once on startup
//
// SDK packages are not bundled. Each adapter imports its package lazily, so a
// framework that has not been installed yet fails its run with a clear
// "framework not installed" error instead of crashing the worker.

import readline from "node:readline";
import { runClaudeAgent } from "./adapters/claude.mjs";
import { runOpenAIAgents } from "./adapters/openai.mjs";

const ADAPTERS = {
  "claude-agent-sdk": runClaudeAgent,
  "openai-agents": runOpenAIAgents,
};

function send(event) {
  process.stdout.write(JSON.stringify(event) + "\n");
}

// emit is the per-request event sink handed to each adapter.
function makeEmit(id) {
  return (event) => send({ id, ...event });
}

async function handleRun(req) {
  const emit = makeEmit(req.id);
  const adapter = ADAPTERS[req.kind];
  if (!adapter) {
    emit({ type: "error", error: `unknown framework kind: ${req.kind}`, final: true });
    return;
  }
  // Overlay per-run env (API keys, base URLs) without leaking across runs is
  // acceptable here: the daemon controls one run at a time per process.
  const restore = applyEnv(req.env);
  try {
    await adapter(req, emit);
  } catch (err) {
    emit({ type: "error", error: describeError(err), final: true });
  } finally {
    restore();
  }
}

function applyEnv(env) {
  if (!env || typeof env !== "object") return () => {};
  const prev = {};
  for (const [k, v] of Object.entries(env)) {
    prev[k] = process.env[k];
    process.env[k] = String(v);
  }
  return () => {
    for (const [k, v] of Object.entries(prev)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  };
}

function describeError(err) {
  if (!err) return "unknown error";
  if (err.code === "ERR_MODULE_NOT_FOUND" || err.code === "MODULE_NOT_FOUND") {
    return `framework not installed: ${err.message}`;
  }
  return err.stack || err.message || String(err);
}

const rl = readline.createInterface({ input: process.stdin });
rl.on("line", (line) => {
  const trimmed = line.trim();
  if (!trimmed) return;
  let req;
  try {
    req = JSON.parse(trimmed);
  } catch {
    send({ type: "error", error: "invalid request json" });
    return;
  }
  switch (req.type) {
    case "run":
      void handleRun(req);
      break;
    case "ping":
      send({ id: req.id, type: "pong" });
      break;
    default:
      send({ id: req.id, type: "error", error: `unknown request type: ${req.type}`, final: true });
  }
});

rl.on("close", () => process.exit(0));

send({ type: "ready" });
