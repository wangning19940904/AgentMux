import { describe, expect, it } from "vitest";
import type { Framework, FrameworkUpdateCheck } from "../../api";
import { targetKey } from "../../components/TargetBadge";
import { frameworkUpdateCandidates } from "./frameworkBulkUpdate";

function framework(kind: string, targetID = "local"): Framework {
  return {
    spec: { kind, display: kind, kind_type: "cli", supported: true, install_supported: true, install_requires_npm: false, update_supported: true },
    installed: true, registered: true, target_id: targetID,
  };
}

function checksFor(items: Framework[]): Record<string, FrameworkUpdateCheck> {
  return Object.fromEntries(items.map((item) => [targetKey(item.target_id, item.spec.kind), {
    kind: item.spec.kind, installed: true, update_available: true,
  }]));
}

describe("framework update candidates", () => {
  it("includes updates across pages while distinguishing the same framework on different machines", () => {
    const items = Array.from({ length: 15 }, (_, index) => framework("codex", `ssh-${index}`));
    const checks = checksFor(items);
    checks["ssh-1::codex"].update_available = false;
    const candidates = frameworkUpdateCandidates(items, checks);
    expect(candidates).toHaveLength(14);
    expect(candidates).toContain(items[14]);
    expect(candidates).not.toContain(items[1]);
  });

  it("excludes uninstalled, unsupported, unchecked, failed checks, and up-to-date items", () => {
    const items = Array.from({ length: 7 }, (_, index) => framework(`framework-${index}`));
    const checks = checksFor(items);
    items[1].installed = false;
    items[2].spec.supported = false;
    items[3].spec.update_supported = false;
    delete checks["local::framework-4"];
    checks["local::framework-5"].error = "unreachable";
    checks["local::framework-6"].update_available = false;
    expect(frameworkUpdateCandidates(items, checks)).toEqual([items[0]]);
    expect(frameworkUpdateCandidates([], {})).toEqual([]);
  });
});
