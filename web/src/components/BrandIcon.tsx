import { Brain, Terminal } from "lucide-react";
import { runtimeLogoURL } from "../panels/agents/runtimeLogoData";

const modelLogos: Record<string, string> = {
  openai: new URL("./model-logos/openai.svg", import.meta.url).href,
  gemini: new URL("./model-logos/gemini-color.svg", import.meta.url).href,
  deepseek: new URL("./model-logos/deepseek-color.svg", import.meta.url).href,
  grok: new URL("./model-logos/grok.svg", import.meta.url).href,
  qwen: new URL("./model-logos/qwen-color.svg", import.meta.url).href,
  doubao: new URL("./model-logos/doubao-color.svg", import.meta.url).href,
  glm: new URL("./model-logos/chatglm-color.svg", import.meta.url).href,
};

const MODEL_BRANDS = [
  { pattern: /claude|anthropic|sonnet|opus|haiku|fable/i, runtime: "claude" },
  { pattern: /gpt|openai|codex|(?:^|\/)o[134](?:[.-]|$)/i, runtime: "openai" },
  { pattern: /gemini|gemma/i, runtime: "gemini" },
  { pattern: /kimi|moonshot/i, runtime: "kimi" },
  { pattern: /deepseek|(?:^|\/)dsh(?:$|[.-])/i, runtime: "deepseek" },
  { pattern: /grok|xai/i, runtime: "grok" },
  { pattern: /qwen/i, runtime: "qwen" },
  { pattern: /doubao|seed/i, runtime: "doubao" },
  { pattern: /glm|zhipu/i, runtime: "glm" },
] as const;

export function BrandIcon({ name, kind = "framework", className = "" }: {
  name: string;
  kind?: "framework" | "model";
  className?: string;
}) {
  const runtime = kind === "model"
    ? MODEL_BRANDS.find((brand) => brand.pattern.test(name))?.runtime ?? name
    : name === "dsh" ? "deepseek" : name;
  const logo = modelLogos[runtime] ?? runtimeLogoURL(runtime);
  return (
    <span className={`brand-icon ${className}`} aria-hidden="true">
      {logo ? <img src={logo} alt="" /> : kind === "model" ? <Brain size={17} /> : <Terminal size={17} />}
    </span>
  );
}
