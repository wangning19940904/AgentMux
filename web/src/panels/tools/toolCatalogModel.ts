import type {
  CLIManagedTool,
  CLILinkedSkillStatus,
  MachineTarget,
  MarketplaceSkill,
  Skill,
  TargetMetadata,
} from "../../api";

export type InstalledToolKind = "cli" | "skill";

export interface InstalledToolRow {
  key: string;
  kind: InstalledToolKind;
  name: string;
  description: string;
  targetID: string;
  targetName: string;
  cli?: CLIManagedTool;
  skill?: Skill;
  linkedSkills: CLILinkedSkillStatus[];
  hasLinkedSkills: boolean;
  needsRepair: boolean;
}

export interface ToolInstallCandidate {
  key: string;
  kind: InstalledToolKind;
  id: string;
  name: string;
  description: string;
  internalOnly: boolean;
  cli?: CLIManagedTool;
  skill?: MarketplaceSkill;
  missingTargetIDs: string[];
}

export function normalizeToolTargets<T extends TargetMetadata>(
  items: T[],
  fallback: { id: string; name: string },
): T[] {
  return items.map((item) => item.target_id
    ? item
    : { ...item, target_id: fallback.id, target_name: fallback.name });
}

export function buildInstalledToolRows(cliItems: CLIManagedTool[], skills: Skill[]): InstalledToolRow[] {
  const claimedSkills = new Set<string>();
  const rows: InstalledToolRow[] = [];

  for (const item of cliItems) {
    if (!item.installed) continue;
    const targetID = item.target_id || "local";
    const targetName = item.target_name || targetID;
    const linkedSkills = item.linked_skills ?? [];
    const declaredSkills = item.spec.linked_skills ?? [];
    for (const linked of linkedSkills) {
      if (!linked.installed) continue;
      claimedSkills.add(skillKey(targetID, linked.spec.id));
      claimedSkills.add(skillKey(targetID, linked.spec.name));
    }
    rows.push({
      key: toolKey(targetID, "cli", item.spec.id),
      kind: "cli",
      name: item.spec.name,
      description: item.spec.note || "",
      targetID,
      targetName,
      cli: item,
      linkedSkills,
      hasLinkedSkills: declaredSkills.length > 0 || linkedSkills.length > 0,
      needsRepair: declaredSkills.length > linkedSkills.length || linkedSkills.some((linked) => !linked.installed || !linked.in_sync),
    });
  }

  for (const skill of skills) {
    const targetID = skill.target_id || "local";
    if (claimedSkills.has(skillKey(targetID, skill.name))) continue;
    rows.push({
      key: toolKey(targetID, "skill", skill.name),
      kind: "skill",
      name: skill.name,
      description: skill.description || "",
      targetID,
      targetName: skill.target_name || targetID,
      skill,
      linkedSkills: [],
      hasLinkedSkills: false,
      needsRepair: false,
    });
  }

  return rows.sort((left, right) => left.name.localeCompare(right.name) || left.targetName.localeCompare(right.targetName));
}

export function buildInstallCandidates(
  cliItems: CLIManagedTool[],
  skills: Skill[],
  marketplace: MarketplaceSkill[],
  targets: MachineTarget[],
): ToolInstallCandidate[] {
  const eligibleTargets = targets.filter((target) => target.online && target.trusted);
  const eligibleIDs = new Set(eligibleTargets.map((target) => target.id));
  const installedSkills = new Set(skills.map((skill) => skillKey(skill.target_id || "local", skill.name)));
  const cliByID = new Map<string, CLIManagedTool[]>();
  for (const item of cliItems) {
    const group = cliByID.get(item.spec.id) ?? [];
    group.push(item);
    cliByID.set(item.spec.id, group);
  }

  const candidates: ToolInstallCandidate[] = [];
  for (const [id, group] of cliByID) {
    const representative = group[0];
    if (!representative) continue;
    const missingTargetIDs = group
      .filter((item) => !item.installed && eligibleIDs.has(item.target_id || "local"))
      .map((item) => item.target_id || "local");
    if (missingTargetIDs.length === 0) continue;
    candidates.push({
      key: `cli:${id}`,
      kind: "cli",
      id,
      name: representative.spec.name,
      description: representative.spec.note || "",
      internalOnly: Boolean(representative.spec.internal_only),
      cli: representative,
      missingTargetIDs: unique(missingTargetIDs),
    });
  }

  for (const skill of marketplace) {
    const missingTargetIDs = eligibleTargets
      .filter((target) => !installedSkills.has(skillKey(target.id, skill.name)))
      .map((target) => target.id);
    if (missingTargetIDs.length === 0) continue;
    candidates.push({
      key: `skill:${skill.repo}:${skill.path}`,
      kind: "skill",
      id: skill.name,
      name: skill.name,
      description: skill.description || "",
      internalOnly: false,
      skill,
      missingTargetIDs,
    });
  }

  return candidates.sort((left, right) => left.name.localeCompare(right.name) || left.kind.localeCompare(right.kind));
}

export function toolKey(targetID: string, kind: InstalledToolKind, id: string) {
  return `${targetID}::${kind}::${id}`;
}

function skillKey(targetID: string, name: string) {
  return `${targetID}::${name.trim().toLowerCase()}`;
}

function unique(values: string[]) {
  return [...new Set(values)];
}
