import type { GrantableResourceType } from "../api";

export interface GrantResource {
  id: string;
  name: string;
  type: GrantableResourceType;
  owner_tenant_id?: string;
  owner_tenant_name?: string;
}

export interface GrantResourceSources {
  agents: Omit<GrantResource, "type">[];
  channels: Omit<GrantResource, "type">[];
  triggers: Omit<GrantResource, "type">[];
  providers: Omit<GrantResource, "type">[];
}

// resourcesForType is the single source of truth for the authorization filter.
// Keeping this outside the component makes it difficult for the dropdown and
// the rendered list to drift apart again.
export function resourcesForType(
  type: GrantableResourceType,
  sources: GrantResourceSources,
): GrantResource[] {
  const key = `${type}s` as keyof GrantResourceSources;
  return sources[key].map((item) => ({ ...item, type }));
}

export function toggleResourceSelection(current: string[], id: string): string[] {
  return current.includes(id)
    ? current.filter((item) => item !== id)
    : [...current, id];
}
