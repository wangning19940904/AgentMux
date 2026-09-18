// Official product icons are bundled locally for offline desktop use.
// Exact upstream URLs are recorded in runtime-logos/sources.json.
const logos = {
  codex: new URL("./runtime-logos/codex.png", import.meta.url).href,
  claude: new URL("./runtime-logos/claude.png", import.meta.url).href,
  cursor: new URL("./runtime-logos/cursor.svg", import.meta.url).href,
  gemini: new URL("./runtime-logos/gemini.png", import.meta.url).href,
  traecli: new URL("./runtime-logos/traecli.png", import.meta.url).href,
  opencode: new URL("./runtime-logos/opencode.png", import.meta.url).href,
  qoder: new URL("./runtime-logos/qoder.png", import.meta.url).href,
  iflow: new URL("./runtime-logos/iflow.ico", import.meta.url).href,
  kimi: new URL("./runtime-logos/kimi.png", import.meta.url).href,
  deepagents: new URL("./runtime-logos/deepagents.png", import.meta.url).href,
};

const aliases: Record<string, keyof typeof logos> = {
  codex: "codex",
  "codex-app": "codex",
  "codex-vscode": "codex",
  "codex-unknown": "codex",
  claude: "claude",
  claudecode: "claude",
  "claude-desktop": "claude",
  "claude-unknown": "claude",
  cursor: "cursor",
  gemini: "gemini",
  traecli: "traecli",
  opencode: "opencode",
  qoder: "qoder",
  iflow: "iflow",
  kimi: "kimi",
  deepagents: "deepagents",
};

export function runtimeLogoURL(runtime: string): string | undefined {
  const brand = aliases[runtime.trim().toLowerCase()];
  return brand ? logos[brand] : undefined;
}
