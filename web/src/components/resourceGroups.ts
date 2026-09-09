import type { Provider } from "../api";

export interface ResourceGroup<T> {
  key: string;
  members: T[];
}

// Keep the original instances: mutations must always address one machine.
export function groupResources<T>(items: T[], identity: (item: T) => string, instance: (item: T) => string, aggregate: boolean): ResourceGroup<T>[] {
  const groups = new Map<string, ResourceGroup<T>>();
  for (const item of items) {
    const key = aggregate ? identity(item) : instance(item);
    const group = groups.get(key);
    if (group) group.members.push(item);
    else groups.set(key, { key, members: [item] });
  }
  return [...groups.values()];
}

export function providerGroupKey(provider: Provider) {
  // An identical ID at a different endpoint is a different provider.
  return JSON.stringify([provider.id, provider.base_url.trim().replace(/\/+$/, "")]);
}

export function selectedGroupMember<T>(group: ResourceGroup<T>, selected: string | undefined, instance: (item: T) => string): T {
  return group.members.find((item) => instance(item) === selected) ?? group.members[0];
}
