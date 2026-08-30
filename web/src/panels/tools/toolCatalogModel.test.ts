import { describe, expect, it } from "vitest";
import type { CLIManagedTool, MachineTarget, MarketplaceSkill, Skill } from "../../api";
import {
  buildInstalledToolRows,
  buildInstallCandidates,
  normalizeToolTargets,
} from "./toolCatalogModel";

function cli(
  id: string,
  targetID: string,
  installed: boolean,
  linked: { installed: boolean; in_sync: boolean } | null = null,
): CLIManagedTool {
  return {
    spec: {
      id,
      name: id === "cis-cli" ? "CIS CLI" : id,
      bin: id,
      package: id,
      note: `${id} description`,
      uninstall_supported: true,
      linked_skills: linked === null ? [] : [{ id: "cis-cli", name: "cis-cli Skill" }],
    },
    installed,
    target_id: targetID,
    target_name: targetID === "local" ? "Local" : "Remote",
    linked_skills: linked === null ? [] : [{
      spec: { id: "cis-cli", name: "cis-cli Skill" },
      installed: linked.installed,
      in_sync: linked.in_sync,
    }],
  };
}

function skill(name: string, targetID: string): Skill {
  return {
    name,
    path: `/skills/${name}/SKILL.md`,
    description: `${name} description`,
    enabled: true,
    target_id: targetID,
    target_name: targetID === "local" ? "Local" : "Remote",
  };
}

const targets: MachineTarget[] = [
  { id: "local", name: "Local", kind: "local", online: true, trusted: true },
  { id: "remote", name: "Remote", kind: "ssh", online: true, trusted: true },
  { id: "offline", name: "Offline", kind: "ssh", online: false, trusted: true },
];

const pdf: MarketplaceSkill = {
  name: "pdf",
  description: "PDF tools",
  source: "openai",
  repo: "openai/skills",
  path: "skills/pdf",
  trusted: true,
  installed: false,
};

describe("installed tool rows", () => {
  it("shows installed items only and merges linked Skills on the same machine", () => {
    const rows = buildInstalledToolRows(
      [cli("cis-cli", "local", true, { installed: true, in_sync: true }), cli("opencli", "remote", false)],
      [skill("cis-cli", "local"), skill("cis-cli", "remote"), skill("pdf", "local")],
    );

    expect(rows.map((row) => row.key)).toEqual([
      "local::cli::cis-cli",
      "remote::skill::cis-cli",
      "local::skill::pdf",
    ]);
    expect(rows[0]).toMatchObject({ hasLinkedSkills: true, needsRepair: false });
    expect(rows.some((row) => row.name === "opencli")).toBe(false);
  });

  it("marks a CLI row for repair when its linked Skill is missing or stale", () => {
    const missing = buildInstalledToolRows([cli("cis-cli", "local", true, { installed: false, in_sync: false })], []);
    const stale = buildInstalledToolRows([cli("cis-cli", "local", true, { installed: true, in_sync: false })], [skill("cis-cli", "local")]);
    expect(missing[0].needsRepair).toBe(true);
    expect(stale[0].needsRepair).toBe(true);
  });
});

describe("install candidates", () => {
  it("offers each catalog item only on online trusted machines where it is missing", () => {
    const candidates = buildInstallCandidates(
      [cli("opencli", "local", true), cli("opencli", "remote", false), cli("opencli", "offline", false)],
      [skill("pdf", "local")],
      [pdf],
      targets,
    );

    expect(candidates.find((candidate) => candidate.key === "cli:opencli")?.missingTargetIDs).toEqual(["remote"]);
    expect(candidates.find((candidate) => candidate.key.startsWith("skill:"))?.missingTargetIDs).toEqual(["remote"]);
  });

  it("fills target metadata for a single-machine response", () => {
    const normalized = normalizeToolTargets([cli("opencli", "", true)], { id: "remote", name: "Remote" });
    expect(normalized[0]).toMatchObject({ target_id: "remote", target_name: "Remote" });
  });
});
