import { useSignal } from "@preact/signals";
import type { CycleTimeHistory } from "../protocol/generated";
import { type UsageRange, usageRanges, usageRangeWords } from "../model/usage";
import { type CycleTimeBreakdown, cycleTimeChart, cycleTimeHeadline, cycleTimeRepositories, durationWords, noStartWords, splitWords } from "../model/cycletime";
import { allRepositories, repoWords } from "../model/velocity";
import { Choice } from "./Choice";
import { Icon } from "./Icon";
import { label, tick, when } from "./UsageCharts";

const width = 600;
const height = 200;
const axis = 64;

export const couldNotReadCycleTime = "Could not read cycle time";

const breakdownOptions: readonly (readonly [CycleTimeBreakdown, string])[] = [["level", "By level"], ["repo", "By repository"]];

export function CycleTimeCharts({ history, failed = false, retry }: {
  history: CycleTimeHistory | undefined; failed?: boolean; retry?: () => void;
}) {
  const range = useSignal<UsageRange>("W");
  const breakdown = useSignal<CycleTimeBreakdown>("level");
  const repo = useSignal("");
  const picked = useSignal<string | null>(null);
  if (!history && failed) {
    return (
      <div class="work-plan-could-not" role="alert">
        <span class="mark" aria-hidden="true"><Icon name="triangle-alert" /></span>
        <p class="strong">{couldNotReadCycleTime}</p>
        {retry && <button type="button" onClick={retry}>Try again</button>}
      </div>
    );
  }
  if (!history) return <p class="quiet">Loading…</p>;
  const repoOptions: [string, string][] = [["", allRepositories]];
  for (const keyRepo of cycleTimeRepositories(history)) {
    repoOptions.push([keyRepo, repoWords(keyRepo)]);
  }
  const chart = cycleTimeChart(history, range.value, breakdown.value, repo.value || null);
  const bar = chart.bars.find((b) => b.stamp === picked.value) ?? null;
  const top = Math.max(...chart.bars.map((b) => b.value), chart.p85, 1);
  const step = (width - axis) / chart.bars.length;
  const y = (value: number) => height - (value / top) * (height - 8);
  return (
    <section class="usage-charts velocity-charts">
      {failed && (
        <p class="quiet small" role="status">{couldNotReadCycleTime}.{" "}
          {retry && <button type="button" class="link" onClick={retry}>Try again</button>}</p>
      )}
      <Choice label="Range" value={range.value} options={usageRanges.map((r) => [r, usageRangeWords[r]] as const)}
        onPick={(r) => { range.value = r; picked.value = null; }} />
      <Choice label="By" value={breakdown.value} options={breakdownOptions}
        onPick={(b) => { breakdown.value = b; }} />
      <Choice label="Repository" value={repo.value} options={repoOptions}
        onPick={(keyRepo) => { repo.value = keyRepo; picked.value = null; }} />
      <div class="usage-headline">
        <p class="quiet small strong">{(bar ? "PICKED BAR" : cycleTimeHeadline).toUpperCase()}</p>
        <p class="usage-figure">{durationWords(bar?.value ?? chart.median)}</p>
        <p class="quiet">{when(chart.range, chart.bars, bar)}</p>
      </div>
      {chart.count === 0 ? (
        <p class="quiet">No pull requests landed in this range.</p>
      ) : (
        <>
          <svg class="usage-bars" viewBox={`0 0 ${width} ${height + 18}`} role="img"
            aria-label={`${cycleTimeHeadline} ${durationWords(chart.median)}`}>
            {[0.5, 1].map((f) => (
              <g key={f}>
                <line class="usage-grid" x1={0} x2={width - axis} y1={y(top * f)} y2={y(top * f)} />
                <text class="usage-axis" x={width - axis + 6} y={y(top * f) + 4}>{durationWords(top * f)}</text>
              </g>
            ))}
            <line class="usage-grid" x1={0} x2={width - axis} y1={height} y2={height} />
            {chart.bars.map((b, i) => (
              <g key={b.stamp} onClick={() => { picked.value = picked.value === b.stamp ? null : b.stamp; }}>
                <rect class="usage-hit" x={i * step} y={0} width={step} height={height} />
                {b.value > 0 && <rect class={`usage-bar${bar && bar !== b ? " dim" : ""}`} x={i * step + step * 0.15}
                  y={y(b.value)} width={step * 0.7} height={height - y(b.value)} rx={2}>
                  <title>{`${label(chart.range, b)}: ${durationWords(b.value)} (${b.count})`}</title>
                </rect>}
                {tick(chart.range, i, chart.bars.length) && (
                  <text class="usage-axis" x={i * step + step / 2} y={height + 14} text-anchor="middle">{label(chart.range, b, true)}</text>
                )}
              </g>
            ))}
            {chart.p85 > 0 && (
              <line class="usage-average" x1={0} x2={width - axis} y1={y(chart.p85)} y2={y(chart.p85)}>
                <title>{`85th percentile: ${durationWords(chart.p85)}`}</title>
              </line>
            )}
          </svg>
          {chart.shares.map((share) => (
            <p key={share.name} class="usage-share"><span>{share.name}</span><span>{durationWords(share.value)}</span></p>
          ))}
          <p class="quiet small">{splitWords(chart)}</p>
          <p class="quiet small">{noStartWords(chart.noStart, chart.repo !== null)}</p>
        </>
      )}
    </section>
  );
}
