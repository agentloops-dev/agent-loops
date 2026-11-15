// The Machines page's list (#5285, epic #5281): for a person with many machines. A search, filter chips with counts (state,
// provider, OS, version, label), a sort, and a table that loads a page at a time from machines/list (#5283). Tick
// rows, then Start, Stop or Delete the ticked ones; a row opens the machine's page. "Last update" says when this list was
// read (#5139). A control plane before machines/2 answers every machine at once, and the page filters them itself.
import { useEffect, useRef, useState } from "preact/hooks";
import type { Store } from "../model/store";
import { Capability } from "../protocol/capabilities.generated";
import type { ControlMachine, MachineCounts, MachineList } from "../protocol/generated";
import { stateWords, versionWords } from "../model/capabilities";
import { machineRows } from "../model/store/shared";
import { machineSessions } from "../model/machineSessions";
import { bulkActions, bulkDeleteQuestion, bulkLabel, chipWords, chips, countsOf, emptyWords, filterTitle, localList, narrowed, noChoice,
  pageQuery, selectedWords, showMoreWords, sortTitle, sorts, toggled, type MachineChoice, type MachineSort } from "../model/machineList";
import { describe } from "../model/errors";
import { go } from "../route";
import { LastUpdate } from "./LastUpdate";
import { ask } from "./MessageKinds";

export function MachinesList({ store }: { store: Store }) {
  const [choice, setChoice] = useState<MachineChoice>(noChoice);
  const [rows, setRows] = useState<ControlMachine[]>([]);
  const [next, setNext] = useState<string | undefined>();
  const [total, setTotal] = useState(0);
  const [counts, setCounts] = useState<MachineCounts | null>(null);
  const [readAt, setReadAt] = useState<number | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const [working, setWorking] = useState(false);
  const paged = store.planeHas(Capability.machines2);
  const macID = store.macHostID.value;
  const hosts = store.hosts.value;
  // The latest read wins: an answer of an older choice is let go.
  const serial = useRef(0);

  const read = async (c: MachineChoice, cursor?: string) => {
    const mine = ++serial.current;
    setLoading(true);
    try {
      if (!paged) {
        const all = machineRows(await store.link.call("machines/list", {}));
        if (mine !== serial.current) return;
        const shown = localList(all, c);
        setRows(shown.slice(0, (cursor ? Number(cursor) : 0) + 50));
        setNext(shown.length > (cursor ? Number(cursor) : 0) + 50 ? String((cursor ? Number(cursor) : 0) + 50) : undefined);
        setTotal(shown.length);
        setCounts(countsOf(all));
      } else {
        const answer = await store.link.call("machines/list", pageQuery(c, cursor));
        if (mine !== serial.current) return;
        const page = answer as MachineList;
        setRows((before) => cursor ? [...before, ...page.machines] : page.machines);
        setNext(page.next);
        setTotal(page.total);
        setCounts(page.counts);
      }
      setReadAt(Date.now());
      setProblem(null);
    } catch (error) {
      if (mine === serial.current) setProblem(describe(error));
    } finally {
      if (mine === serial.current) setLoading(false);
    }
  };

  // Typing waits a moment; a chip or a sort reads at once. A machine joining, leaving or changing state reads again.
  useEffect(() => {
    const timer = setTimeout(() => void read(choice), choice.search ? 250 : 0);
    return () => clearTimeout(timer);
  }, [choice, hosts.map((h) => `${h.id}:${h.state}`).join(",")]);
  // A ticked machine that is no longer in the list is let go.
  useEffect(() => {
    setPicked((now) => {
      const ids = new Set<string>(rows.map((r) => r.id));
      const kept = [...now].filter((id) => ids.has(id));
      return kept.length === now.size ? now : new Set(kept);
    });
  }, [rows]);

  const selected = rows.filter((r) => picked.has(r.id));
  const actions = bulkActions(selected, macID);
  const tick = (id: string, on: boolean) => setPicked((now) => { const n = new Set(now); if (on) n.add(id); else n.delete(id); return n; });
  const allTicked = rows.length > 0 && selected.length === rows.length;
  const afterwards = () => { setPicked(new Set()); void read(choice); };

  const start = async () => {
    setWorking(true);
    await Promise.all(actions.start.map((h) => store.startHost(h.id)));
    setWorking(false);
    afterwards();
  };
  const stop = async () => {
    setWorking(true);
    for (const h of actions.stop) {
      const asked = await store.providers.stop(h.id);
      if (typeof asked === "string") store.say(asked);
      else if (asked && await ask(`Stop ${h.name}? ${asked.question}`)) {
        const again = await store.providers.stop(h.id, true);
        if (typeof again === "string") store.say(again);
      }
    }
    setWorking(false);
    afterwards();
  };
  // Delete lists the unpushed work of every machine first, as Delete on one machine does (#3979).
  const remove = async () => {
    setWorking(true);
    const previews = await Promise.all(actions.delete.map(async (h) => ({ h, preview: await store.deletePreview(h.id) })));
    const unpushed = previews.flatMap(({ h, preview }) => preview?.unknown ? [`${h.name}: ${preview.unknown}`]
      : (preview?.unpushed ?? []).map((w) => `${h.name}: ${w.folder}`));
    const yes = await ask(bulkDeleteQuestion(actions.delete.map((h) => h.name), unpushed));
    if (yes) for (const h of actions.delete) await store.deleteHost(h.id);
    setWorking(false);
    afterwards();
  };

  const shownChips = chips(counts, choice);
  return (
    <div class="machines-list">
      <div class="machines-tools" role="search">
        <input type="search" aria-label="Search machines" placeholder="Search by name, id or label" value={choice.search}
          onInput={(e) => setChoice({ ...choice, search: (e.target as HTMLInputElement).value })} />
        <label class="select-label">Sort
          <select value={choice.sort} onChange={(e) => setChoice({ ...choice, sort: (e.target as HTMLSelectElement).value as MachineSort })}>
            {sorts.map((s) => <option key={s} value={s}>{sortTitle[s]}</option>)}
          </select>
        </label>
        {readAt !== null && <LastUpdate at={readAt} />}
      </div>
      {shownChips.length > 0 && (
        <div class="machine-chips" role="group" aria-label="Filters">
          {shownChips.map((chip) => (
            <button key={`${chip.key}:${chip.value}`} type="button" class={`chip${chip.on ? " on" : ""}`} aria-pressed={chip.on}
              title={filterTitle[chip.key]} onClick={() => setChoice(toggled(choice, chip.key, chip.value))}>
              {chip.key === "labels" ? `# ${chipWords(chip)}` : chipWords(chip)}
            </button>
          ))}
          {narrowed(choice) && <button type="button" class="link small" onClick={() => setChoice({ ...noChoice, sort: choice.sort })}>Clear</button>}
        </div>
      )}
      {selected.length > 0 && (
        <div class="machine-bulk" role="toolbar" aria-label="Actions on the selected machines">
          <span class="strong">{selectedWords(selected.length)}</span>
          <button type="button" disabled={working || actions.start.length === 0} onClick={() => void start()}>
            {bulkLabel("Start", actions.start.length, selected.length)}</button>
          <button type="button" disabled={working || actions.stop.length === 0} onClick={() => void stop()}>
            {bulkLabel("Stop", actions.stop.length, selected.length)}</button>
          <button type="button" disabled={working || actions.delete.length === 0} onClick={() => void remove()}>
            {bulkLabel("Delete…", actions.delete.length, selected.length)}</button>
          <button type="button" class="link small" onClick={() => setPicked(new Set())}>Clear selection</button>
        </div>
      )}
      {problem && <p class="hint failure" role="status">{problem}</p>}
      <div class="data-table-wrap" role="region" aria-label="Machines" tabIndex={0}>
        <table class="data-table console-table" aria-label="Machines" aria-busy={loading}>
          <thead>
            <tr>
              <th scope="col"><input type="checkbox" aria-label="Select every machine shown" checked={allTicked}
                onChange={(e) => setPicked((e.target as HTMLInputElement).checked ? new Set(rows.map((r) => r.id)) : new Set())} /></th>
              <th scope="col">Machine</th><th scope="col">OS</th><th scope="col">Version</th>
              <th scope="col" class="num">Working</th>
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 && <tr class="empty"><td colSpan={5}>{readAt === null ? "Loading…" : emptyWords(narrowed(choice))}</td></tr>}
            {rows.map((h) => {
              const name = h.id === macID ? "This Mac" : h.name;
              const working = machineSessions(store.agents.value[h.id] ?? []).running;
              return (
                <tr key={h.id} class="opens" aria-selected={picked.has(h.id)}
                  onClick={(e) => { if (!(e.target as HTMLElement).closest("button, a, input, select")) go({ nav: "machines", section: `machine-${h.id}` }); }}>
                  <td><input type="checkbox" aria-label={`Select ${name}`} checked={picked.has(h.id)}
                    onChange={(e) => tick(h.id, (e.target as HTMLInputElement).checked)} /></td>
                  <td>
                    <button type="button" class="link strong" onClick={() => go({ nav: "machines", section: `machine-${h.id}` })}>{name}</button>
                    <br /><span class={`fleet-state ${h.state} small`}>{stateWords(h)}</span>
                  </td>
                  <td>{h.platform}</td>
                  <td>{versionWords(h)}</td>
                  <td class="num">{working === 0 ? "—" : working}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      <p class="quiet small" role="status">
        {rows.length < total ? `${rows.length} of ${total} machines` : `${total} ${total === 1 ? "machine" : "machines"}`}
        {next !== undefined && <> · <button type="button" class="link small" disabled={loading} onClick={() => void read(choice, next)}>
          {showMoreWords(rows.length, total)}</button></>}
      </p>
    </div>
  );
}
