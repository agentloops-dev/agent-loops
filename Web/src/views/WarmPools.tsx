// Settings ▸ Machine providers ▸ ‹provider› ▸ Warm Pools (#5354, epic #5353, board 5), as the window's WarmPoolsSection:
// a card per pool, with how many machines are ready of min and max, how many are being set up, the last seven days'
// claims, what the idle ones cost and a line for each machine; New Warm Pool… and Edit… on one form, and Delete…. Only a
// provider that makes machines has pools. The pools are read again every 30 s while the page is open, and the section
// says when it last read them (#5139).
import { useEffect, useState } from "preact/hooks";
import type { ProviderID, ProviderSummary, WarmPool, WarmPoolSet } from "../protocol/generated";
import type { Store } from "../model/store";
import { describe } from "../model/errors";
import { formFields, type FormField } from "../model/machineFromRepository";
import {
  claimsLine, daysOf, formCost, formProblem, hourly, idleCost, nearestReplace, poolChip, poolMachineLine, poolStateLine, poolSummary,
  projectName, readyLine, replaceChoices, runsOf, runsWords, type PoolRuns,
} from "../model/warmPools";
import { LastUpdate } from "./LastUpdate";
import { Modal } from "./Modal";
import { ask } from "./MessageKinds";

const poolsIntro = "A warm pool keeps machines ready: made, joined, with Agents, runtimes and the projects you choose, and signed in to nothing. New Machine takes one in seconds, and the pool makes another.";

/** Reads the pools when the page opens and every 30 s while it is open: their machines set themselves up and are claimed. */
export function useWarmPools(store: Store): void {
  useEffect(() => {
    void store.providers.loadPools();
    const timer = setInterval(() => void store.providers.loadPools(), 30_000);
    return () => clearInterval(timer);
  }, [store]);
}

/** Whether a provider has a Warm Pools section: it makes machines, and the control plane has pools. */
export function hasWarmPools(store: Store, provider: ProviderSummary): boolean {
  return store.providers.poolsHere.value && provider.capabilities.includes("create");
}

export function WarmPoolsSection({ store, provider }: { store: Store; provider: ProviderSummary }) {
  const pools = store.providers.pools.value.filter((p) => p.provider === provider.id);
  const readAt = store.providers.poolsReadAt.value;
  const [editing, setEditing] = useState<{ pool?: WarmPool } | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const remove = async (pool: WarmPool) => {
    if (!await ask(`Delete ${pool.name}? Its ready machines are deleted. Machines already claimed stay as they are.`)) return;
    setProblem(await store.providers.deletePool(pool.id));
  };
  return (
    <div class="warm-pools" aria-label={`${provider.name}: warm pools`}>
      <div class="warm-pools-head">
        <h4>Warm Pools</h4>
        {readAt !== null && <LastUpdate at={readAt} />}
        <span class="grow" />
        <button type="button" disabled={provider.mayUse === false} onClick={() => setEditing({})}>New Warm Pool…</button>
      </div>
      {pools.length === 0 && <p class="hint small">{poolsIntro}</p>}
      {problem && <p class="hint failure">{problem}</p>}
      {pools.map((pool) => (
        <PoolCard key={pool.id} store={store} pool={pool} edit={() => setEditing({ pool })} remove={() => void remove(pool)} />
      ))}
      {editing && <WarmPoolDialog store={store} provider={provider} {...(editing.pool ? { pool: editing.pool } : {})} close={() => setEditing(null)} />}
    </div>
  );
}

function PoolCard({ store, pool, edit, remove }: { store: Store; pool: WarmPool; edit: () => void; remove: () => void }) {
  const [now, setNow] = useState(() => new Date());
  const machines = pool.machines ?? [];
  // A second's tick while it holds machines, so their ages move.
  useEffect(() => {
    if (machines.length === 0) return;
    const timer = setInterval(() => setNow(new Date()), 1_000);
    return () => clearInterval(timer);
  }, [machines.length]);
  const price = store.providers.poolPrices.value[pool.provider]?.[pool.size ?? ""] ?? null;
  const why = poolStateLine(pool);
  return (
    <section class="warm-pool" aria-label={pool.name}>
      <div class="warm-pool-title">
        <div class="grow">
          <p class="strong">{pool.name} <span class={`warm-pool-chip${pool.state === "stopped" ? " stopped" : ""}`}>{poolChip(pool)}</span></p>
          <p class="small quiet">{poolSummary(pool)}</p>
        </div>
        <button type="button" onClick={edit}>Edit…</button>
        <button type="button" class="destructive" onClick={remove}>Delete…</button>
      </div>
      <div class="warm-pool-figures">
        <div>
          <p class="small quiet">Ready</p>
          <p><span class="number">{pool.ready ?? 0}</span><span class="small quiet">{readyLine(pool)}</span></p>
        </div>
        <div>
          <p class="small quiet">Setting up</p>
          <p><span class="number">{pool.settingUp ?? 0}</span></p>
        </div>
        <div>
          <p class="small quiet">Last 7 days</p>
          <p class="small">{claimsLine(pool.claims)}</p>
          <p class="small quiet">{idleCost(pool, price)}</p>
        </div>
      </div>
      {why && <p class={`small ${pool.state === "stopped" ? "failure" : "quiet"}`}>{why}</p>}
      {machines.length > 0 && (
        <ul class="warm-pool-machines">
          {machines.map((m) => (
            <li key={m.name} class="small"><span class={`dot ${m.state}`} aria-hidden="true" />{poolMachineLine(m, now)}</li>
          ))}
        </ul>
      )}
    </section>
  );
}

/** A path on the Mac from a folder's file URL, as a pool keeps its projects. */
function pathOf(url: string): string {
  try { return decodeURIComponent(new URL(url).pathname).replace(/\/+$/, ""); } catch { return url; }
}

/** The provider's field for a key (size, region): one named so, else for size its first choice. */
function fieldOf(fields: readonly FormField[], key: string): FormField | undefined {
  return fields.find((f) => f.key.toLowerCase() === key) ?? (key === "size" ? fields.find((f) => f.choices) : undefined);
}

const hhmm = /^([01]\d|2[0-3]):[0-5]\d$/;

/** New Warm Pool… and Edit… (board 5, note 3), as the window's WarmPoolSheet: name, size, region, min ready and max size,
 * the age after which a ready machine is replaced, the hours it runs, and the projects on the Mac it sends, with what it
 * costs. Save keeps it; the control plane makes or deletes at once. */
function WarmPoolDialog({ store, provider, pool, close }: { store: Store; provider: ProviderSummary; pool?: WarmPool; close: () => void }) {
  const [name, setName] = useState(pool?.name ?? "");
  const [size, setSize] = useState(pool?.size ?? "");
  const [region, setRegion] = useState(pool?.region ?? "");
  const [minReady, setMinReady] = useState(pool?.minReady ?? 1);
  const [maxSize, setMaxSize] = useState(pool?.maxSize ?? 2);
  const [replaceAfter, setReplaceAfter] = useState(nearestReplace(pool?.replaceAfter));
  const [runs, setRuns] = useState<PoolRuns>(runsOf(pool?.hours?.days));
  const [from, setFrom] = useState(pool?.hours?.from ?? "08:00");
  const [to, setTo] = useState(pool?.hours?.to ?? "19:00");
  const [sending, setSending] = useState<Set<string>>(new Set(pool?.projects ?? []));
  const [sizes, setSizes] = useState<FormField | undefined>();
  const [regions, setRegions] = useState<FormField | undefined>();
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  // The Mac's projects, and any the pool sends that this page does not list (kept unless unticked).
  const mac = store.macHostID.value;
  const listed = [...new Set((store.folders.value[mac] ?? []).filter((f) => f.exists).map((f) => pathOf(f.folder.folder)))];
  const projects = [...listed, ...(pool?.projects ?? []).filter((p) => !listed.includes(p))];
  useEffect(() => {
    let live = true;
    store.providers.options(provider.id as ProviderID).then((o) => {
      if (!live) return;
      const fields = formFields(o.schema);
      const values = (o.values ?? {}) as Record<string, unknown>;
      const s = fieldOf(fields, "size");
      const r = fieldOf(fields, "region");
      setSizes(s);
      setRegions(r);
      const first = (f: FormField | undefined) => (f ? String(values[f.key] ?? f.choices?.[0] ?? "") : "");
      if (!pool?.size) setSize(first(s));
      if (!pool?.region) setRegion(first(r));
    }, (error: unknown) => { if (live) setProblem(describe(error)); })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, []);
  const hoursProblem = runs !== "always" && (!hhmm.test(from) || !hhmm.test(to)) ? "From and To are times, such as 08:00." : null;
  const refused = formProblem(name, minReady, maxSize) ?? hoursProblem;
  const price = hourly(size ? sizes?.costs?.[size] : undefined);
  const save = async () => {
    if (refused || working || loading) return;
    const days = daysOf(runs);
    const set: WarmPoolSet = {
      ...(pool ? { id: pool.id } : {}), name: name.trim(), scope: "user", provider: provider.id as ProviderID,
      ...(size ? { size } : {}), ...(region ? { region } : {}), ...(pool?.runtimes ? { runtimes: pool.runtimes } : {}),
      projects: projects.filter((p) => sending.has(p)), minReady, maxSize, replaceAfter,
      ...(days ? { hours: { days, from, to, timezone: zone } } : {}),
    };
    setWorking(true);
    setProblem(null);
    const why = await store.providers.setPool(set);
    setWorking(false);
    if (why) setProblem(why); else close();
  };
  const toggle = (path: string, on: boolean) => {
    const next = new Set(sending);
    if (on) next.add(path); else next.delete(path);
    setSending(next);
  };
  const choice = (field: FormField | undefined, value: string, set: (v: string) => void, label: string, hint?: string) => (
    <label>{label}
      {field?.choices
        ? <select value={value} onChange={(e) => set((e.target as HTMLSelectElement).value)}>
            {field.choices.map((c) => <option key={c} value={c}>{field.costs?.[c] ? `${c} — ${field.costs[c]}` : c}</option>)}
          </select>
        : <input type="text" value={value} placeholder={hint} autocomplete="off" spellcheck={false}
            onInput={(e) => set((e.target as HTMLInputElement).value)} />}
    </label>
  );
  const title = pool ? "Edit Warm Pool" : `New Warm Pool on ${provider.name}`;
  // An empty name only keeps Save off: it is not said in red before anything is typed.
  const shown = problem ?? (name ? refused : formProblem("-", minReady, maxSize) ?? hoursProblem);
  return (
    <Modal label={title} close={close}>
      <form class="sheet-body connect-sheet provider-sheet" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <h2>{title}</h2>
        <fieldset class="token-form" disabled={working}>
          <label>Name
            <input type="text" value={name} placeholder="dev-large" autofocus autocomplete="off" spellcheck={false}
              onInput={(e) => setName((e.target as HTMLInputElement).value)} />
          </label>
          {loading ? <p class="small quiet">Reading the provider’s choices…</p> : (
            <>
              {choice(sizes, size, setSize, "Size")}
              {choice(regions, region, setRegion, "Region", "the provider’s default")}
            </>
          )}
          <label>Min ready
            <input type="number" min={0} max={20} value={minReady} onInput={(e) => setMinReady(Number((e.target as HTMLInputElement).value) || 0)} />
          </label>
          <label>Max size (ready or setting up)
            <input type="number" min={1} max={20} value={maxSize} onInput={(e) => setMaxSize(Number((e.target as HTMLInputElement).value) || 0)} />
          </label>
          <label>Replace a ready machine after
            <select value={String(replaceAfter)} onChange={(e) => setReplaceAfter(Number((e.target as HTMLSelectElement).value))}>
              {replaceChoices.map((c) => <option key={c.seconds} value={String(c.seconds)}>{c.label}</option>)}
            </select>
          </label>
          <label>Runs
            <select value={runs} onChange={(e) => setRuns((e.target as HTMLSelectElement).value as PoolRuns)}>
              {(Object.keys(runsWords) as PoolRuns[]).map((r) => <option key={r} value={r}>{runsWords[r]}</option>)}
            </select>
          </label>
          {runs !== "always" && (
            <>
              <label>From <input type="time" value={from} onInput={(e) => setFrom((e.target as HTMLInputElement).value)} /></label>
              <label>To <input type="time" value={to} onInput={(e) => setTo((e.target as HTMLInputElement).value)} /></label>
              <p class="small quiet">In {zone}. Outside these hours its ready machines are deleted and none are made.</p>
            </>
          )}
          <fieldset class="pool-projects">
            <legend>Projects to send</legend>
            {projects.length === 0 && <p class="small quiet">No projects on the Mac.</p>}
            {projects.map((p) => (
              <label key={p} title={p}><input type="checkbox" checked={sending.has(p)}
                onChange={(e) => toggle(p, (e.target as HTMLInputElement).checked)} />{projectName(p)}</label>
            ))}
            <p class="small quiet">From the Mac, at their last commit, then each one’s .agents/setup.sh. A claim adds the rest.</p>
          </fieldset>
        </fieldset>
        {shown && <p class="small failure" role="alert">{shown}</p>}
        <p class="small quiet">{formCost(minReady, maxSize, price)}</p>
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={!!refused || working || loading}>Save</button>
        </div>
      </form>
    </Modal>
  );
}
