import { useMemo, useState, type KeyboardEvent, type PointerEvent } from "react";
import { useI18n } from "../../i18n";
import { formatShare, formatTokens } from "./format";
import type { Period } from "./usageQuery";
import { OTHER_SLOT, bucketLabel, niceCeiling, stackTotals, type TrendSeries } from "./usageTrend";

const WIDTH = 1000;
const HEIGHT = 200;
const TOP = 8;
const MAX_TICKS = 7;

function slotClass(slot: number) {
  return slot === OTHER_SLOT ? "series-other" : `series-${slot}`;
}

// UsageTrendChart draws token usage per bucket as stacked columns: the column
// is the bucket's total and its segments are the series. Hovering, or moving
// with the arrow keys while the chart has focus, reads out one bucket.
export function UsageTrendChart({ keys, series, period, totalTokens, loading }: {
  keys: string[];
  series: TrendSeries[];
  period: Period;
  totalTokens: number;
  loading: boolean;
}) {
  const { t } = useI18n();
  const [active, setActive] = useState<number | null>(null);
  const totals = useMemo(() => stackTotals(series, keys.length), [series, keys.length]);
  const ceiling = niceCeiling(Math.max(...totals, 0));
  const singleDay = keys.length > 0 && keys[0].slice(0, 10) === keys[keys.length - 1].slice(0, 10);
  const step = keys.length > 0 ? WIDTH / keys.length : WIDTH;
  // Columns keep a gap while there is room for one; a dense chart reads as a
  // continuous profile instead.
  const barWidth = keys.length <= 120 ? step * 0.72 : step;
  const plotHeight = HEIGHT - TOP;
  const scale = (value: number) => (value / ceiling) * plotHeight;

  const columns = useMemo(() => keys.map((_, position) => {
    let offset = 0;
    return series.flatMap((item) => {
      const value = item.values[position] ?? 0;
      if (value <= 0) return [];
      const height = scale(value);
      const y = HEIGHT - offset - height;
      offset += height;
      // A hairline of surface separates stacked segments tall enough to spare it.
      return [{ id: item.id, slot: item.slot, y: height > 3 ? y + 1 : y, height: height > 3 ? height - 1 : height }];
    });
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }), [keys, series, ceiling]);

  const ticks = useMemo(() => {
    if (keys.length === 0) return [];
    const count = Math.min(MAX_TICKS, keys.length);
    const positions = Array.from({ length: count }, (_, index) => Math.round(index * (keys.length - 1) / Math.max(1, count - 1)));
    return [...new Set(positions)].map((position) => ({
      position,
      label: bucketLabel(keys[position], period, { singleDay }),
    }));
  }, [keys, period, singleDay]);

  const selected = active !== null && active < keys.length ? active : null;

  function positionFromPointer(event: PointerEvent<HTMLDivElement>) {
    const bounds = event.currentTarget.getBoundingClientRect();
    if (bounds.width <= 0 || keys.length === 0) return null;
    const ratio = (event.clientX - bounds.left) / bounds.width;
    return Math.min(keys.length - 1, Math.max(0, Math.floor(ratio * keys.length)));
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (keys.length === 0) return;
    const last = keys.length - 1;
    const current = selected ?? last;
    let next: number | null = null;
    if (event.key === "ArrowLeft") next = Math.max(0, current - 1);
    else if (event.key === "ArrowRight") next = Math.min(last, current + 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = last;
    else if (event.key === "Escape") { setActive(null); return; }
    if (next === null) return;
    event.preventDefault();
    setActive(next);
  }

  const empty = series.length === 0;
  const tooltipLeft = selected === null ? 0 : ((selected + 0.5) / keys.length) * 100;

  return (
    <div className={`usage-trend-wrap${loading ? " is-loading" : ""}`}>
      {series.length > 1 && (
        <div className="trend-series-legend">
          {series.map((item) => (
            <span key={item.id}>
              <i className={`trend-series-swatch ${slotClass(item.slot)}`} />
              <strong title={item.label}>{item.label}</strong>
              <small>{formatTokens(item.tokens)} · {formatShare(item.tokens, totalTokens)}</small>
            </span>
          ))}
        </div>
      )}
      <div className="usage-chart">
        <div className="usage-chart-axis" aria-hidden="true">
          <span>{formatTokens(ceiling)}</span>
          <span>{formatTokens(ceiling / 2)}</span>
          <span>0</span>
        </div>
        <div
          className="usage-chart-plot"
          role="group"
          tabIndex={empty ? -1 : 0}
          aria-label={t("usage.trendChartLabel", { count: keys.length })}
          onPointerMove={(event) => setActive(positionFromPointer(event))}
          onPointerLeave={() => setActive(null)}
          onBlur={() => setActive(null)}
          onFocus={() => { if (selected === null && keys.length > 0) setActive(keys.length - 1); }}
          onKeyDown={onKeyDown}
        >
          <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} preserveAspectRatio="none" aria-hidden="true">
            <line x1="0" x2={WIDTH} y1={HEIGHT} y2={HEIGHT} className="usage-chart-grid" />
            {selected !== null && (
              <rect x={selected * step} y={0} width={step} height={HEIGHT} className="usage-chart-cursor" />
            )}
            {columns.map((segments, position) => segments.map((segment) => (
              <g key={`${keys[position]}-${segment.id}`} className={slotClass(segment.slot)}>
                <rect
                  x={position * step + (step - barWidth) / 2}
                  y={segment.y}
                  width={barWidth}
                  height={Math.max(segment.height, 0.75)}
                  className={`usage-chart-bar ${slotClass(segment.slot)}${selected !== null && selected !== position ? " dimmed" : ""}`}
                />
                <rect
                  x={position * step + (step - barWidth) / 2}
                  y={segment.y}
                  width={barWidth}
                  height={Math.min(segment.height, 4)}
                  className={`usage-chart-bar-cap${selected !== null && selected !== position ? " dimmed" : ""}`}
                />
              </g>
            )))}
          </svg>
          {empty && <span className="usage-chart-empty">{loading ? t("common.loading") : t("usage.noUsageInRange")}</span>}
          {selected !== null && !empty && (
            <div
              className={`usage-chart-tooltip${tooltipLeft > 60 ? " align-right" : ""}`}
              style={tooltipLeft > 60 ? { right: `${100 - tooltipLeft}%` } : { left: `${tooltipLeft}%` }}
              role="status"
            >
              <div className="usage-chart-tooltip-title">{bucketLabel(keys[selected], period, { singleDay, long: true })}</div>
              {series.length > 1 && series.filter((item) => (item.values[selected] ?? 0) > 0).map((item) => (
                <div className="usage-chart-tooltip-row" key={item.id}>
                  <i className={`trend-series-key ${slotClass(item.slot)}`} />
                  <strong>{formatTokens(item.values[selected])}</strong>
                  <span>{item.label}</span>
                </div>
              ))}
              <div className="usage-chart-tooltip-row total">
                <strong>{formatTokens(totals[selected])}</strong>
                <span>{t("overview.trend.total")}</span>
              </div>
            </div>
          )}
        </div>
        <div className="usage-chart-ticks" aria-hidden="true">
          {ticks.map((tick) => (
            <span key={tick.position} style={{ left: `${((tick.position + 0.5) / keys.length) * 100}%` }}>{tick.label}</span>
          ))}
        </div>
      </div>
    </div>
  );
}
