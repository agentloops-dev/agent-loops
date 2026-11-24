// The one machine picker (#5287, epic #5281): a button that opens a search field, the pinned and recently used machines, then
// the rest grouped by label, each with a state dot. It asks machines/list with the typed words (#5283), so a person with
// many machines never loads them all; a control plane before machines/2 answers them all and the picker filters them itself.
import { useEffect, useRef, useState } from "preact/hooks";
import type { Store } from "../model/store";
import { Capability } from "../protocol/capabilities.generated";
import type { ControlMachine, MachineList } from "../protocol/generated";
import { localList, noChoice } from "../model/machineList";
import { machineRows } from "../model/store/shared";
import { describe } from "../model/errors";
import { pickerDot, pickerLimit, pickerQuery, pickerWords, pinnedMachines, recentMachines, rememberMachine, sections, togglePin } from "../model/machinePicker";

export function MachinePicker({ store, value, onPick, label, skip, any, anyDisabled, placeholder = "Choose a machine", disabled, class: className }: {
  store: Store;
  /** The machine chosen, or null for none (or `any`). */
  value: string | null;
  onPick: (id: string | null) => void;
  /** The name for assistive tech, and the button's when nothing is chosen. */
  label: string;
  /** Machines left out, as the one a session is on. */
  skip?: (h: ControlMachine) => boolean;
  /** A first row that picks no machine, as "Every machine". */
  any?: string | undefined;
  /** The `any` row is shown but cannot be picked, as an Auto with no machine that fits. */
  anyDisabled?: boolean;
  placeholder?: string;
  disabled?: boolean;
  class?: string;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [page, setPage] = useState<ControlMachine[]>([]);
  const [total, setTotal] = useState(0);
  const [problem, setProblem] = useState<string | null>(null);
  const [pinned, setPinned] = useState<string[]>(() => pinnedMachines(globalThis.localStorage));
  const [recent, setRecent] = useState<string[]>(() => recentMachines(globalThis.localStorage));
  const serial = useRef(0);
  const root = useRef<HTMLDivElement>(null);
  const hosts = store.hosts.value;
  const current = value ? hosts.find((h) => h.id === value) ?? page.find((h) => h.id === value) : undefined;
  const paged = store.planeHas(Capability.machines2);

  useEffect(() => {
    if (!open) return;
    const mine = ++serial.current;
    const timer = setTimeout(async () => {
      try {
        if (paged) {
          const answer = await store.link.call("machines/list", pickerQuery(search)) as MachineList;
          if (mine !== serial.current) return;
          setPage(answer.machines);
          setTotal(answer.total);
        } else {
          const shown = localList(machineRows(await store.link.call("machines/list", {})), { ...noChoice, search });
          if (mine !== serial.current) return;
          setPage(shown.slice(0, pickerLimit));
          setTotal(shown.length);
        }
        setProblem(null);
      } catch (error) {
        if (mine === serial.current) setProblem(describe(error));
      }
    }, search ? 200 : 0);
    return () => clearTimeout(timer);
  }, [open, search, hosts.map((h) => `${h.id}:${h.state}`).join(",")]);

  // Escape or a click outside closes it.
  useEffect(() => {
    if (!open) return;
    const away = (e: Event) => { if (root.current && !root.current.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", away);
    return () => document.removeEventListener("pointerdown", away);
  }, [open]);

  const pick = (id: string | null) => {
    if (id) setRecent(rememberMachine(globalThis.localStorage, id));
    setOpen(false);
    setSearch("");
    onPick(id);
  };
  const pin = (id: string) => {
    const next = togglePin(globalThis.localStorage, id);
    if (next) setPinned(next);
    setProblem((now) => next ? (now === pickerWords.full ? null : now) : pickerWords.full);
  };
  const shown = sections(page, pinned, recent, skip);
  return (
    <div class={`machine-picker${className ? ` ${className}` : ""}`} ref={root}
      onKeyDown={(e) => { if (e.key === "Escape" && open) { e.stopPropagation(); setOpen(false); } }}>
      <button type="button" class="machine-picker-button" aria-haspopup="listbox" aria-expanded={open} aria-label={label}
        disabled={disabled} onClick={() => setOpen(!open)}>
        {current && <span class={`dot ${pickerDot(current)}`} aria-hidden="true" />}
        <span class="name">{current?.name ?? (value ? value : any ?? placeholder)}</span>
        <span class="caret" aria-hidden="true">▾</span>
      </button>
      {open && (
        <div class="machine-picker-panel">
          <input type="search" autoFocus value={search} placeholder={pickerWords.search} aria-label={pickerWords.search}
            onInput={(e) => setSearch((e.currentTarget as HTMLInputElement).value)} />
          <div class="machine-picker-list" role="listbox" aria-label={label}>
            {any && !search && (
              <button type="button" role="option" aria-selected={value === null} class="machine-picker-row" disabled={anyDisabled} onClick={() => pick(null)}>
                <span class="name">{any}</span>
              </button>
            )}
            {shown.map((s) => (
              <div key={s.title} role="group" aria-label={s.title}>
                <p class="machine-picker-title">{s.title}</p>
                {s.machines.map((h) => (
                  <div key={h.id} class="machine-picker-line">
                    <button type="button" role="option" aria-selected={h.id === value} class="machine-picker-row" onClick={() => pick(h.id)}>
                      <span class={`dot ${pickerDot(h)}`} aria-hidden="true" />
                      <span class="name">{h.name}</span>
                      <span class="state">{h.state}</span>
                    </button>
                    <button type="button" class={`machine-picker-pin${pinned.includes(h.id) ? " on" : ""}`}
                      aria-pressed={pinned.includes(h.id)} aria-label={(pinned.includes(h.id) ? pickerWords.unpin : pickerWords.pin)(h.name)}
                      onClick={() => pin(h.id)}>★</button>
                  </div>
                ))}
              </div>
            ))}
            {problem ? <p class="failure small" role="alert">{problem}</p>
              : shown.length === 0 && <p class="hint small">{pickerWords.none}</p>}
            {total > page.length && <p class="hint small">{pickerWords.more(page.length, total)}</p>}
          </div>
        </div>
      )}
    </div>
  );
}
