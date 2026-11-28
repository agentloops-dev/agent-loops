// The control plane as a machine (#6592, the web port of the window's #6591; App/Sources/Hosts/ControlPlanePage.swift,
// ControlCapacityStrip.swift): for an admin, its machine record first in Machines, named Control plane, with its level;
// its page, with the level and why, load per core, memory and I/O pressure, then Memory, Process and Store, and "Last
// update" (#5139); the same figures as a section on the page of a machine that also runs agents; and a strip across the
// page while a copy is near or at capacity. The control plane keeps serving at every level: the strip only tells.
import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import type { Store } from "../model/store";
import type { ControlCapacityCopy, ControlMachine } from "../protocol/generated";
import { Capability } from "../protocol/capabilities.generated";
import * as cp from "../model/controlPlaneMachine";
import { levelClass } from "../model/capacity";
import { lastUpdateLine } from "../model/lastUpdate";
import { go } from "../route";
import { LastUpdate } from "./LastUpdate";
import { MachineIcon } from "./MachineRow";
import { Strip } from "./Strip";

/** The person sees the control plane as a machine: an admin, on a control plane that answers control/capacity. */
export function showsControlPlaneMachine(store: Store): boolean {
  return store.person.value?.role === "admin" && store.planeHas(Capability.console1);
}

/** The machine runs a copy of the control plane now, and the person sees it as one. */
export function runsControlPlane(store: Store, host: string): boolean {
  const rows = store.hosts.value;
  return showsControlPlaneMachine(store) && cp.runsCopy(rows.find((h) => h.id === host), rows);
}

/** The machine runs only the control plane: its row and page are the control plane's. */
export function isControlPlaneOnly(store: Store, host: string): boolean {
  return runsControlPlane(store, host) && cp.controlOnly(store.hosts.value.find((h) => h.id === host));
}

/** Reads now and every 15 s while a control plane's page or section is drawn and the tab is seen. */
function useFollow(store: Store, host: string) {
  useEffect(() => {
    const read = () => { if (document.visibilityState === "visible") void store.loadControlCapacity(); };
    read();
    const timer = setInterval(read, cp.lookSeconds * 1000);
    return () => clearInterval(timer);
  }, [store, host]);
}

/** "Last update just now", with a spinner while a read is under way (#5139). */
function Freshness() {
  const at = cp.readAt.value;
  return (
    <span class="control-freshness">
      {cp.refreshing.value && <span class="spinner small" aria-label="Refreshing capacity" />}
      {at !== null && <LastUpdate at={at} />}
    </span>
  );
}

/** The level's tint: the vouched green at OK, amber near, red full. */
const tintOf = (look: ControlCapacityCopy | null) => (look === null ? "quiet" : look.level === "ok" ? "vouched" : levelClass(look.level));

/** The figures (gate 2 of #6593): the level and its sentence, four figures across, then Memory, Process and Store. Before
 * the first look, why there is none; after a read that failed, the last look under a line that says how old it is. */
function Figures({ look }: { look: ControlCapacityCopy | null }) {
  const failure = cp.failure.value;
  const at = cp.readAt.value;
  const none = cp.noReadingNowWords();
  return (
    <div class="control-figures">
      {failure && <p class="warning small" role="status">{at === null ? failure : `${failure} ${lastUpdateLine(at)}`}</p>}
      {look ? (
        <>
          <p class={`control-level ${levelClass(look.level)}`} role="status">
            <span class={`control-dot ${tintOf(look)}`} aria-hidden="true" />{cp.sentence(look)}
          </p>
          <div class="control-tiles">
            {cp.summary(look).map((f) => (
              <div class="control-tile" key={f.label}>
                <span class="quiet small">{f.label}</span>
                <span class={`control-tile-value${f.label === "Capacity" ? ` ${levelClass(look.level)}` : ""}`}>{f.value}</span>
                {f.detail && <span class="quiet small">{f.detail}</span>}
              </div>
            ))}
          </div>
          {cp.sections(look).map((s) => (
            <div class="control-section" key={s.title}>
              <span class="strong">{s.title}</span>
              <span class="quiet">{s.figures.map((f) => `${f.label} ${f.value}`).join("  ·  ")}</span>
            </div>
          ))}
          <p class="quiet small">{cp.foot}</p>
        </>
      ) : at !== null || failure !== null ? (
        <div class="control-none">
          <span class="strong">{none.line}</span>
          <span class="quiet">{none.detail}</span>
        </div>
      ) : <p class="hint">Loading…</p>}
    </div>
  );
}

/** The page of a machine that runs only the control plane, from its row in Machines. */
export function ControlPlanePage({ store, host }: { store: Store; host: string }) {
  useFollow(store, host);
  const rows = store.hosts.value;
  const row = rows.find((h) => h.id === host);
  return (
    <div class="control-plane-page">
      <div class="control-plane-head">
        <span class="quiet">{cp.metaLine(row, rows, location.host || null)}</span>
        <Freshness />
      </div>
      <Figures look={cp.lookOn(host, rows)} />
    </div>
  );
}

/** The control plane on the page of a machine that also runs agents: the same figures, as a section. */
export function ControlPlaneSection({ store, host }: { store: Store; host: string }) {
  useFollow(store, host);
  return (
    <section aria-labelledby="control-plane-head" class="control-plane-section">
      <div class="section-head-row">
        <h3 class="section-head" id="control-plane-head">Control plane</h3>
        <Freshness />
      </div>
      <Figures look={cp.lookOn(host, store.hosts.value)} />
    </section>
  );
}

/** The page's title for a machine that runs only the control plane: "Control plane", or with its label beside several. */
export function controlPlaneTitle(store: Store, host: string): string {
  const rows = store.hosts.value;
  return cp.titleOf(rows.find((h) => h.id === host), rows);
}

/** A machine that runs only the control plane, under Machines (#6591): "Control plane", its icon in its level's colour as
 * a host's capacity is (#4748), and under it the level. No sessions, start, stop or delete: the control plane refuses
 * them (#6609). A click opens its page. */
export function ControlPlaneRow({ store, host, on, open }: { store: Store; host: string; on: boolean; open: () => void }) {
  const rows = store.hosts.value;
  const row = rows.find((h) => h.id === host);
  const look = cp.lookOn(host, rows);
  const none = cp.noReadingNowWords();
  const line = look ? cp.level(look) : none.line;
  const tone = look === null ? "stopped" : look.level === "full" ? "failure" : look.level === "near" ? "attention" : "accent";
  return (
    <div class="machine-row control-plane-row" title={look ? cp.sentence(look, cp.label(row, rows)) : none.detail}>
      <div class="machine-row-body">
        <button type="button" class={`sub-app-row${on ? " on" : ""}`} aria-current={on ? "page" : undefined} onClick={open}>
          <span class="machine-name"><MachineIcon kind="rack" tone={tone} />{cp.titleOf(row, rows)}
            <span class="visually-hidden">, {line}</span></span>
        </button>
        <p class={`machine-need small ${look && look.level !== "ok" ? levelClass(look.level) : "quiet"}`} role="status">{line}</p>
      </div>
    </div>
  );
}

/** Under a machine that runs the control plane in Machines' list: where it is, then its level in the level's tint. */
export function ControlPlaneLines({ store, row, state }: { store: Store; row: ControlMachine; state: string }) {
  const rows = store.hosts.value;
  const look = cp.lookOn(row.id, rows);
  const none = cp.noReadingNowWords();
  const long = cp.rowLine(look, none.line);
  const fits = useFits(long);
  return (
    <>
      <span class="quiet small">{cp.controlOnly(row) ? cp.metaLine(row, rows, location.host || null) : `Runs the control plane \u00B7 ${state}`}</span>
      {/* The whole line when it fits, else the level and its main figure (#7366), as the window's ViewThatFits. */}
      <span class="control-row-fit" ref={fits.box}>
        <span class={`small control-row-line ${look ? levelClass(look.level) || "" : "quiet"}`} title={cp.rowHelp(look, none.detail)}>
          <span class={`control-dot ${tintOf(look)}`} aria-hidden="true" />
          <span ref={fits.text} class={fits.on ? undefined : "control-row-measure"} aria-hidden={fits.on ? undefined : "true"}>{long}</span>
          {!fits.on && <span>{cp.shortRowLine(look, none.line)}</span>}
        </span>
      </span>
    </>
  );
}

/** Whether a line fits its box on one line, measured again when the box changes size or the words change. */
function useFits(words: string) {
  const box = useRef<HTMLSpanElement>(null);
  const text = useRef<HTMLSpanElement>(null);
  const [on, setOn] = useState(true);
  useLayoutEffect(() => {
    const b = box.current;
    const t = text.current;
    if (!b || !t) return;
    // The dot and its gap are 12 px.
    const measure = () => setOn(t.offsetWidth + 12 <= b.clientWidth);
    measure();
    const watch = new ResizeObserver(measure);
    watch.observe(b);
    return () => watch.disconnect();
  }, [words]);
  return { box, text, on };
}

/** Across the page while a copy of the control plane is near or at capacity, for an admin (#4582's words): why, in the
 * control plane's words; Show Control Plane opens its machine, What to Do the guide (#6589). Hidden while the page
 * cannot reach the control plane (#7363): the level is then the last one known, not now. */
export function ControlCapacityStrip({ store, down }: { store: Store; down: boolean }) {
  if (!showsControlPlaneMachine(store)) return null;
  const look = cp.warning(cp.looks(), down);
  if (!look) return null;
  const rows = store.hosts.value;
  const machine = look.machine ?? rows.find((h) => h.roles?.includes("control"))?.id;
  return (
    <Strip tone="warning" name="control-capacity-strip"
      line={cp.sentence(look, cp.label(rows.find((h) => h.id === machine), rows))}
      primary={machine ? { label: cp.stripWords.show, title: cp.stripWords.showHelp, run: () => go({ nav: "machines", section: `machine-${machine}` }) } : null}
      second={{ label: cp.stripWords.what, title: cp.stripWords.whatHelp, run: () => { window.open(cp.guide, "_blank", "noopener"); } }} />
  );
}
