export type NavigationGroupLike<T extends string = string> = {
  id: string;
  items: ReadonlyArray<{ id: T }>;
};

export type NavigationSearchItem<T extends string = string> = {
  id: T;
  label: string;
  groupLabel?: string;
  keywords?: ReadonlyArray<string>;
  groupKeywords?: ReadonlyArray<string>;
};

function normalizeSearchText(value: string): string {
  return value.normalize("NFKC").trim().toLocaleLowerCase();
}

function compactSearchText(value: string): string {
  return normalizeSearchText(value).replace(/[^\p{L}\p{N}]+/gu, "");
}

export function searchNavigationItems<T extends NavigationSearchItem>(
  items: ReadonlyArray<T>,
  query: string,
): T[] {
  const normalizedQuery = normalizeSearchText(query);
  if (!normalizedQuery) return [];

  const tokens = normalizedQuery.split(/\s+/).filter(Boolean);
  const compactQuery = compactSearchText(normalizedQuery);
  return items
    .map((item, index) => {
      const label = normalizeSearchText(item.label);
      const id = normalizeSearchText(item.id);
      const keywords = (item.keywords ?? []).map(normalizeSearchText).filter(Boolean);
      const groupLabel = normalizeSearchText(item.groupLabel ?? "");
      const groupKeywords = (item.groupKeywords ?? []).map(normalizeSearchText).filter(Boolean);
      const searchableText = [label, id, ...keywords, groupLabel, ...groupKeywords].filter(Boolean);
      const compactSearchableText = searchableText.map(compactSearchText);
      const tokenMatch = tokens.every((token) => {
        const compactToken = compactSearchText(token);
        return searchableText.some((value) => value.includes(token))
          || (compactToken.length > 0 && compactSearchableText.some((value) => value.includes(compactToken)));
      });
      const compactPhraseMatch = compactQuery.length > 1
        && compactSearchableText.some((value) => value.includes(compactQuery));
      if (!tokenMatch && !compactPhraseMatch) {
        return null;
      }

      const compactLabel = compactSearchText(label);
      const compactID = compactSearchText(id);
      const compactKeywords = keywords.map(compactSearchText);
      const compactGroupLabel = compactSearchText(groupLabel);
      const compactGroupKeywords = groupKeywords.map(compactSearchText);
      const isExact = (value: string, compactValue: string) => (
        value === normalizedQuery || (compactQuery.length > 0 && compactValue === compactQuery)
      );
      const startsWithQuery = (value: string, compactValue: string) => (
        value.startsWith(normalizedQuery)
        || (compactQuery.length > 0 && compactValue.startsWith(compactQuery))
      );
      const includesQuery = (value: string, compactValue: string) => (
        value.includes(normalizedQuery)
        || (compactQuery.length > 0 && compactValue.includes(compactQuery))
      );

      let score = 10;
      if (isExact(label, compactLabel)) score = 0;
      else if (isExact(id, compactID)) score = 1;
      else if (keywords.some((value, keywordIndex) => isExact(value, compactKeywords[keywordIndex]))) score = 2;
      else if (startsWithQuery(label, compactLabel)) score = 3;
      else if (startsWithQuery(id, compactID)) score = 4;
      else if (keywords.some((value, keywordIndex) => startsWithQuery(value, compactKeywords[keywordIndex]))) score = 5;
      else if (includesQuery(label, compactLabel)) score = 6;
      else if (isExact(groupLabel, compactGroupLabel)) score = 7;
      else if (groupKeywords.some((value, keywordIndex) => isExact(value, compactGroupKeywords[keywordIndex]))) score = 8;
      else if (includesQuery(groupLabel, compactGroupLabel)) score = 9;

      return { item, index, score };
    })
    .filter((match): match is { item: T; index: number; score: number } => match !== null)
    .sort((left, right) => left.score - right.score || left.index - right.index)
    .map((match) => match.item);
}

export function navigationGroupForTab<G extends NavigationGroupLike>(
  groups: ReadonlyArray<G>,
  tab: string,
): G | null {
  return groups.find((group) => group.items.some((item) => item.id === tab)) ?? null;
}

export function secondaryNavigationForTab<G extends NavigationGroupLike>(
  groups: ReadonlyArray<G>,
  tab: string,
  overviewTab: string,
) {
  const group = navigationGroupForTab(groups, tab);
  return {
    groupID: group?.id ?? null,
    open: tab !== overviewTab && group !== null,
  };
}

export function primaryGroupDestination<T extends string>(
  group: NavigationGroupLike<T>,
  currentTab: string,
): T | null {
  return group.items.some((item) => item.id === currentTab)
    ? null
    : group.items[0]?.id ?? null;
}
