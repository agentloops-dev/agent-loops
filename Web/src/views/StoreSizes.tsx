// Reports ▸ Store sizes (#6907): the stores of the control plane and of each machine in one table, from the cache first
// and then from each place at once (principles: stale while revalidate). A place that does not answer keeps its last
// rows, and the banner names it. A read measures nothing, so there is nothing to start here.
import { useEffect, useMemo, useRef } from "preact/hooks";
import { useSignal } from "@preact/signals";
import { Capability } from "../protocol/capabilities.generated";
import { hostAnswers } from "../model/capabilities";
import { adminOf } from "../model/console";
import type { Store } from "../model/store";
import { keepWork, keptWorkRead } from "../model/workCache";
import { averageNote, cardsOf, emptyTitle, emptyWords, everyStore, failedWords, filtered, filtersOf, firstRows, limitsOf, limitTitle, limitWords,
  measuredWords, memberNote, moreRows, mostUsed, placesOf, planeSource, problemLines, problemWords, readAtOf, readOnlyNote, readPlace,
  recordsWords, refreshingWords, rowsOf, showingWords, sizeWords, staleWords, statusWords, within, type PlaceStores, type Source, type StoreRow } from "../model/storeSizes";
import { Choice } from "./Choice";
import { DataTable, type Column } from "./DataTable";
import { LastUpdate } from "./LastUpdate";
import { MessageBanner } from "./MessageKinds";

const cachePage = "storeSizes";
const minute = 60_000;

export function StoreSizesPage({ store, down }: { store: Store; down: boolean }) {
  const mac = store.macHostID.value;
  const hosts = [...store.hosts.value].sort((a, b) => Number(b.id === mac) - Number(a.id === mac) || a.name.localeCompare(b.name));
  const places = placesOf(hosts.map((h) => ({ id: h.id, name: h.name, answers: hostAnswers(h.state), has: store.has(h.id, Capability.storesSizes1) })));
  const read = useSignal<Record<string, Pick<Source, "got" | "at" | "problem">>>({});
  const reading = useSignal(false);
  const filter = useSignal(everyStore);
  const most = useSignal(firstRows);
  const now = useSignal(Date.now());
  const turn = useRef(0);
  useEffect(() => { const t = setInterval(() => { now.value = Date.now(); }, minute); return () => clearInterval(t); }, []);

  const load = async () => {
    const mine = ++turn.current;
    const current = () => turn.current === mine;
    const set = (id: string, got: Pick<Source, "got" | "at" | "problem">) => {
      if (current()) { read.value = { ...read.peek(), [id]: { ...read.peek()[id], ...got } }; now.value = Date.now(); }
    };
    reading.value = true;
    // What this browser read before, drawn at once.
    await Promise.all(places.map(async (p) => {
      if (read.peek()[p.id]?.got) return;
      const kept = await keptWorkRead<PlaceStores>(cachePage, p.id).catch(() => undefined);
      if (kept && !read.peek()[p.id]?.got) set(p.id, { got: kept.value, at: kept.at });
    }));
    if (!down) {
      await Promise.all(places.map(async (p) => {
        if (p.not) return set(p.id, { problem: p.not });
        try {
          const got = await within(readPlace((q) => p.id === planeSource ? store.link.call("reports/storeSizes", q)
            : store.link.call("stores/list", q, p.id)));
          if (!current()) return;
          set(p.id, { got, at: Date.now(), problem: undefined });
          keepWork(cachePage, p.id, got);
        } catch (error) {
          set(p.id, { problem: problemWords(error) });
        }
      }));
    }
    if (current()) reading.value = false;
  };
  const key = places.map((p) => `${p.id}${p.not ?? ""}`).join(" ");
  useEffect(() => { void load(); }, [key, down]);
  // A read still on its way when the page closes sets nothing.
  useEffect(() => () => { turn.current += 1; }, []);

  const sources: Source[] = places.map((p) => ({ id: p.id, name: p.name, ...read.value[p.id] }));
  const rows = useMemo(() => rowsOf(sources, now.value), [read.value, now.value, key]);
  const cards = useMemo(() => cardsOf(sources, rows), [rows]);
  const at = readAtOf(sources);
  const problems = reading.value ? [] : problemLines(sources, now.value);
  const options = filtersOf(rows);
  const picked = options.some(([f]) => f === filter.value) ? filter.value : everyStore;
  const shown = filtered(rows, picked);
  const update = at !== undefined && <> <LastUpdate at={at} /></>;
  const empty = !reading.value && problems.length === 0 && at !== undefined && !cards.any;
  const stale = !empty && rows.some((r) => r.old);
  const refresh = (
    <button type="button" class="prominent" disabled={reading.value || down} onClick={() => void load()}>{reading.value ? "Refreshing…" : "Refresh"}</button>
  );
  const bar = reading.value ? at !== undefined && <MessageBanner tone="neutral" busy>{refreshingWords}{update}</MessageBanner>
    : problems.length > 0 ? (
      <MessageBanner tone="attention">
        {failedWords}{update}
        <ul class="stores-problems">{problems.map((p) => <li key={p}>{p}</li>)}</ul>
      </MessageBanner>
    ) : stale ? <MessageBanner tone="neutral">{staleWords}{update}</MessageBanner>
    : at !== undefined && <p class="quiet small"><LastUpdate at={at} /></p>;
  return (
    <div class="reports-pane stores-page">
      <div class="reports-head">
        <p class="quiet small">See how much space each store uses and when it was measured.</p>
        {!empty && refresh}
      </div>
      {bar}
      {rows.length === 0 && !empty && problems.length === 0 && <p class="hint">{down ? "The control plane can’t be reached." : "Loading…"}</p>}
      {empty && (
        <div class="stores-empty">
          <strong>{emptyTitle}</strong>
          <p class="quiet">{emptyWords}</p>
          {refresh}
        </div>
      )}
      {!empty && rows.length > 0 && (
        <>
          <ul class="delivery-cards stores-cards">
            <li class="delivery-card"><span>Stores measured</span><strong class="delivery-value">{cards.measured}</strong><span class="quiet small">{cards.unmeasured}</span></li>
            <li class="delivery-card"><span>Total measured size</span><strong class="delivery-value">{cards.bytes}</strong><span class="quiet small">Across measured stores</span></li>
            <li class="delivery-card"><span>Over configured limit</span>
              <strong class={`delivery-value${cards.over > 0 ? " stores-over" : ""}`}>{cards.overWords}</strong><span class="quiet small">{cards.overKinds}</span></li>
          </ul>
          <div class="reports-head">
            <h2 class="section-head">Store inventory</h2>
            <Choice label="Filter stores" value={picked} options={options} onPick={(f) => { filter.value = f; most.value = firstRows; }} />
          </div>
          <DataTable id="storeSizes" label="Store inventory" columns={columns(now.value)} rows={shown} rowKey={(r) => r.key} most={most.value}
            order={{ column: "bytes", dir: "desc" }} empty="No store is like this." />
          <div class="reports-head">
            <p class="quiet small">{showingWords(Math.min(most.value, shown.length), shown.length)} · {averageNote}</p>
            {shown.length > most.value && <button type="button" onClick={() => { most.value += moreRows; }}>Show More</button>}
          </div>
        </>
      )}
      <p class="quiet small">{readOnlyNote}{adminOf(store) === false && ` ${memberNote}`}</p>
    </div>
  );
}

const columns = (now: number): Column<StoreRow>[] => [
  { id: "store", title: "Store", sort: (r) => r.store.id, cell: (r) => <><code>{r.store.id}</code><br /><span class="quiet small">{r.where}</span></> },
  { id: "kind", title: "Kind", sort: (r) => r.store.kind, cell: (r) => r.store.kind },
  { id: "bytes", title: "Measured size", num: true, first: "desc", sort: (r) => r.store.bytes, cell: (r) => sizeWords(r.store.bytes) },
  { id: "records", title: "Records", num: true, first: "desc", sort: (r) => r.store.records, cell: (r) => recordsWords(r.store.records) },
  { id: "average", title: "Avg. record", num: true, first: "desc", sort: (r) => r.store.averageRecordBytes, cell: (r) => sizeWords(r.store.averageRecordBytes) },
  { id: "limit", title: "Limit used", first: "desc", sort: (r) => mostUsed(r.store), cell: (r) => (
    <ul class="stores-limits" title={limitTitle(r.store)}>
      {limitsOf(r.store).map((l) => (
        <li key={l.kind}>
          {l.percent !== undefined && <span class={`report-bar${l.percent > 100 ? " over" : ""}`} aria-hidden="true"><span style={{ width: `${Math.max(1, Math.min(100, l.percent))}%` }} /></span>}
          <span class="small">{limitWords(l)}</span>
        </li>
      ))}
    </ul>
  ) },
  { id: "measured", title: "Last measured", first: "desc", sort: (r) => r.measured, cell: (r) => measuredWords(r, now) },
  { id: "status", title: "Status", sort: (r) => statusWords(r), cell: (r) => (
    <><span class={`stores-status${r.status === "over" ? " stores-over" : r.status === "within" ? "" : " attention"}`}>{statusWords(r)}</span>
      {r.store.why && <><br /><span class="quiet small">{r.store.why}</span></>}</>
  ) },
];
