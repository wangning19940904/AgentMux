import type { UsageBucket } from "../api";

type ModelTrendSeries = { id: string; label: string; tokens: number; values: number[] };

export function buildModelTrendSeries(buckets: UsageBucket[], t: (key: string) => string): ModelTrendSeries[] {
  const models = new Map<string, ModelTrendSeries>();
  function add(id: string, label: string, tokens: number, index: number) {
    if (tokens <= 0) return;
    const series = models.get(id) ?? { id, label, tokens: 0, values: Array(buckets.length).fill(0) };
    series.tokens += tokens;
    series.values[index] += tokens;
    models.set(id, series);
  }
  buckets.forEach((bucket, index) => {
    let attributedTokens = 0;
    for (const model of bucket.by_model ?? []) {
      add(`model:${model.model}`, model.model.trim() || t("overview.unknownModel"), model.tokens, index);
      attributedTokens += model.tokens;
    }
    const totals = bucket.totals;
    const totalTokens = totals.input_tokens + totals.output_tokens + totals.cache_read_tokens + totals.cache_write_tokens;
    // Older machines may return totals without per-model buckets. Keep that
    // usage visible without inventing a model's share of each hour.
    const missingTokens = totalTokens - attributedTokens;
    if (missingTokens > Number.EPSILON * Math.max(1, totalTokens) * 8) {
      add("unattributed-model", t("overview.trend.unattributedModel"), missingTokens, index);
    }
  });
  const ordered = [...models.values()].sort((left, right) => right.tokens - left.tokens || left.id.localeCompare(right.id));
  const visible = ordered.slice(0, 5);
  const hidden = ordered.slice(5);
  if (hidden.length > 0) {
    visible.push({
      id: "other-models",
      label: t("overview.trend.other"),
      tokens: hidden.reduce((sum, model) => sum + model.tokens, 0),
      values: buckets.map((_, index) => hidden.reduce((sum, model) => sum + model.values[index], 0)),
    });
  }
  return visible;
}
