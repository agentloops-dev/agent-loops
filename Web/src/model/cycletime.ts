import type { CycleTimeBucket, CycleTimeHistory, CycleTimeSample } from "../protocol/generated";
import { mergeTiers, usageSlots, type HistoryTiers, type UsageRange } from "./usage";
import { repoWords } from "./velocity";

export type CycleTimeBreakdown = "level" | "repo";

export interface CycleTimeBar {
  stamp: string;
  start: Date;
  value: number;
  count: number;
  noStart: number;
}

export interface CycleTimeShare {
  name: string;
  value: number;
  count: number;
}

export interface CycleTimeChart {
  range: UsageRange;
  breakdown: CycleTimeBreakdown;
  repo: string | null;
  bars: CycleTimeBar[];
  shares: CycleTimeShare[];
  median: number;
  p85: number;
  count: number;
  noStart: number;
  coding: number;
  pr: number;
}

const emptyBucket = (): CycleTimeBucket => ({ samples: [], noStart: 0 });

function addBucket(into: CycleTimeBucket, b: CycleTimeBucket): void {
  into.samples.push(...b.samples);
  into.noStart += b.noStart;
}

function wrap(tier: Record<string, CycleTimeBucket>): Record<string, Record<string, CycleTimeBucket>> {
  return Object.fromEntries(Object.entries(tier).map(([key, bucket]) => [key, { all: bucket }]));
}

function unwrap(tier: Record<string, Record<string, CycleTimeBucket>>): Record<string, CycleTimeBucket> {
  return Object.fromEntries(Object.entries(tier).map(([key, buckets]) => [key, buckets.all ?? emptyBucket()]));
}

export function cycleTimeHere(history: CycleTimeHistory): CycleTimeHistory {
  const tiers: HistoryTiers<CycleTimeBucket> = {
    hours: wrap(history.hours),
    days: wrap(history.days),
    now: history.now,
    ...(history.timeZone === undefined ? {} : { timeZone: history.timeZone }),
  };
  const merged = mergeTiers([tiers], emptyBucket, addBucket);
  return {
    hours: unwrap(merged.hours),
    days: unwrap(merged.days),
    now: merged.now,
    ...(merged.timeZone === undefined ? {} : { timeZone: merged.timeZone }),
  };
}

function percentile(sorted: readonly number[], fraction: number): number {
  if (sorted.length === 0) return 0;
  const rank = (sorted.length - 1) * fraction;
  const lower = Math.floor(rank);
  const upper = Math.ceil(rank);
  return sorted[lower]! + (sorted[upper]! - sorted[lower]!) * (rank - lower);
}

function medians(samples: readonly CycleTimeSample[]): number[] {
  return samples.map((sample) => sample.cycle).sort((a, b) => a - b);
}

function shareName(sample: CycleTimeSample, breakdown: CycleTimeBreakdown): string {
  if (breakdown === "repo") return repoWords(sample.repo);
  return sample.level ? sample.level.charAt(0).toUpperCase() + sample.level.slice(1) : "No level";
}

export function cycleTimeChart(history: CycleTimeHistory, range: UsageRange,
                               breakdown: CycleTimeBreakdown = "level",
                               repo: string | null = null): CycleTimeChart {
  const slots = usageSlots(history.now, range);
  const tier = range === "D" ? history.hours : history.days;
  const all: CycleTimeSample[] = [];
  let noStart = 0;
  const bars = slots.map((slot) => {
    const samples: CycleTimeSample[] = [];
    let bucketNoStart = 0;
    for (const key of slot.keys) {
      const bucket = tier[key];
      if (!bucket) continue;
      samples.push(...bucket.samples.filter((sample) => repo === null || sample.repo === repo));
      bucketNoStart += bucket.noStart;
    }
    all.push(...samples);
    noStart += bucketNoStart;
    const cycles = medians(samples);
    return { stamp: slot.stamp, start: slot.start, value: percentile(cycles, 0.5), count: samples.length, noStart: bucketNoStart };
  });
  const cycles = medians(all);
  const coding = all.flatMap((s) => s.coding === undefined ? [] : [s.coding]).sort((a, b) => a - b);
  const pr = all.flatMap((s) => s.pr === undefined ? [] : [s.pr]).sort((a, b) => a - b);
  const groups = new Map<string, CycleTimeSample[]>();
  for (const sample of all) {
    const name = shareName(sample, breakdown);
    const list = groups.get(name) ?? [];
    list.push(sample);
    groups.set(name, list);
  }
  const shares = [...groups].map(([name, samples]) => ({
    name, value: percentile(medians(samples), 0.5), count: samples.length,
  })).sort((a, b) => b.value - a.value || a.name.localeCompare(b.name));
  return {
    range, breakdown, repo, bars, shares,
    median: percentile(cycles, 0.5),
    p85: percentile(cycles, 0.85),
    count: all.length,
    noStart,
    coding: percentile(coding, 0.5),
    pr: percentile(pr, 0.5),
  };
}

export const cycleTimeHeadline = "Median cycle time";

const durationNumber = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });

export function durationWords(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0m";
  const minutes = seconds / 60;
  if (minutes < 60) return `${Math.max(1, Math.round(minutes))}m`;
  const hours = seconds / 3600;
  if (hours < 24) return `${durationNumber.format(hours)}h`;
  const days = hours / 24;
  return `${durationNumber.format(days)} ${days === 1 ? "day" : "days"}`;
}

export function noStartWords(count: number, acrossAllRepositories = false): string {
  const across = acrossAllRepositories ? " across all repositories" : "";
  return count === 0 ? "No PRs without a recorded start."
    : `${count} ${count === 1 ? "PR" : "PRs"} without a recorded start${across}.`;
}

export function cycleTimeRepositories(history: CycleTimeHistory): string[] {
  const keys = new Set<string>();
  for (const bucket of Object.values(history.hours)) {
    for (const sample of bucket.samples) keys.add(sample.repo);
  }
  for (const bucket of Object.values(history.days)) {
    for (const sample of bucket.samples) keys.add(sample.repo);
  }
  return [...keys].sort((a, b) => repoWords(a).localeCompare(repoWords(b)));
}

export function splitWords(chart: CycleTimeChart): string {
  if (chart.count === 0 || chart.coding === 0 || chart.pr === 0) return "Coding and PR time unavailable.";
  return `${durationWords(chart.coding)} coding · ${durationWords(chart.pr)} PR`;
}
