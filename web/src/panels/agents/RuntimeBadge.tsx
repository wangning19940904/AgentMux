import { Terminal } from "lucide-react";
import { runtimeLabel } from "./agentUtils";
import { runtimeLogoURL } from "./runtimeLogoData";

export function RuntimeBadge({ runtime, emptyLabel = "" }: { runtime: string; emptyLabel?: string }) {
  const logo = runtimeLogoURL(runtime);
  return (
    <span className="runtime-badge">
      <span className={`runtime-logo${logo ? "" : " fallback"}`} aria-hidden="true">
        {logo ? <img src={logo} alt="" /> : <Terminal size={15} />}
      </span>
      <span className="runtime-name">{runtime ? runtimeLabel(runtime) : emptyLabel}</span>
    </span>
  );
}
