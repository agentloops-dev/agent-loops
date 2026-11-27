// Make This the Control Plane… on a machine's page (#6614, gate 2 of epic #6608), as the window's
// ControlMoveSection (App/Sources/Hosts/ControlMoveSection.swift): for an admin, on a machine that does not run the control plane now. The machine's checks
// (control/eligible) are read as the page opens: with a reason, the button is off and the reasons are
// under it. During a move to this machine, and for a day after, its steps; a failed one says which step,
// and Try Again…. The strip across the page says the move while it runs, and for a day after.
import { useEffect, useState } from "preact/hooks";
import type { Store } from "../model/store";
import type { ControlCapacityCopy, ControlEligible, ControlMachine } from "../protocol/generated";
import { Capability } from "../protocol/capabilities.generated";
import { closeMove, controlMoveWords as words, forwardingLine, happens, meetsLine, moveLine, moveTo,
  moves, movesHeardAt, question, runningMove, running, stepStates, steps, stripMove, takeMove } from "../model/controlMove";
import type { ControlMove as Move } from "../protocol/generated";
import { failedWords } from "../model/console";
import { go } from "../route";
import { LastUpdate } from "./LastUpdate";
import { Modal } from "./Modal";
import { Strip } from "./Strip";

/** The person sees the move: an admin, and a control plane that moves. */
export function showsControlMove(store: Store): boolean {
  return store.person.value?.role === "admin" && store.planeHas(Capability.controlMove1);
}

/** The control plane's copies, each naming the machine that runs it (#7393): which machine runs it now. Read
 * as the page opens, and again when a move is heard; null until a read works, and the role says it until then. */
export function useControlCopies(store: Store, on: boolean): ControlCapacityCopy[] | null {
  const [copies, setCopies] = useState<ControlCapacityCopy[] | null>(null);
  const heard = moves.value;
  const asks = on && store.planeHas(Capability.console1);
  useEffect(() => {
    if (!asks) return;
    let gone = false;
    store.link.call("control/capacity", {}).then(
      (board) => { if (!gone) setCopies(board.copies); },
      () => { /* The last copies, or the role, say it until a read works. */ });
    return () => { gone = true; };
  }, [store, asks, heard]);
  return copies;
}

/** The page's section, on a machine that does not run the control plane. */
export function ControlMoveSection({ store, machine }: { store: Store; machine: ControlMachine }) {
  const [answer, setAnswer] = useState<ControlEligible | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [readAt, setReadAt] = useState<number | null>(null);
  const [checking, setChecking] = useState(false);
  const [asking, setAsking] = useState(false);
  useEffect(() => {
    let gone = false;
    setChecking(true);
    store.link.call("control/eligible", { machine: machine.id }).then(
      (a) => { if (!gone) { setAnswer(a); setFailure(null); setReadAt(Date.now()); } },
      (error) => { if (!gone) setFailure(failedWords(error)); },
    ).finally(() => { if (!gone) setChecking(false); });
    return () => { gone = true; };
  }, [store, machine.id]);

  const move = moveTo(machine.id);
  const heard = move ? movesHeardAt.value : readAt;
  const another = runningMove();
  const ok = answer?.ok === true && another === null;
  return (
    <section class="control-move" aria-labelledby="control-move-head">
      <h3 class="section-head" id="control-move-head">
        {words.section}
        {checking && <span class="spinner small" aria-label="Checking" />}
        {/* During a move, when the page last heard it; else when the checks were read. */}
        {heard !== null && <> <LastUpdate at={heard} /></>}
      </h3>
      {move ? (
        <>
          <MoveSteps move={move} />
          {!running(move) && <p class={move.phase === "failed" ? "failure" : "vouched"}>{moveLine(move)}</p>}
          {forwardingLine(move) && <p class="quiet small">{forwardingLine(move)}</p>}
          {move.phase === "failed" && (
            <p><button type="button" disabled={another !== null} title={`Check ${machine.name} again, then move the control plane to it`}
              onClick={() => setAsking(true)}>{words.tryAgain}</button></p>
          )}
        </>
      ) : (
        <>
          <p><button type="button" disabled={!ok}
            title={ok ? `Move the control plane, its store and its key, to ${machine.name}` : `${machine.name} can’t take the control plane now`}
            onClick={() => setAsking(true)}>{words.make}</button></p>
          {failure && <p class="hint warning">{failure}</p>}
          {another ? <p class="hint">{words.anotherRuns}</p>
            : answer?.ok ? <p class="hint">{meetsLine(answer)}</p>
            : answer ? <ul class="control-move-why">{answer.reasons.map((r) => <li key={r.reason}>{r.text}</li>)}</ul>
            : !failure && <p class="hint">{words.checking}</p>}
        </>
      )}
      {asking && <ControlMoveDialog store={store} machine={machine} address={answer?.address} frozenSeconds={answer?.frozenSeconds} close={() => setAsking(false)} />}
    </section>
  );
}

/** The steps of a move, in the order it runs them. */
export function MoveSteps({ move }: { move: Move }) {
  const states = stepStates(move);
  return (
    <ol class="control-move-steps">
      {steps.map((step, i) => {
        const state = states[i] ?? "todo";
        return <li key={step.phase} class={`step-${state}`} aria-label={`${step.title}: ${spoken[state]}`}>{step.title}</li>;
      })}
    </ol>
  );
}

const spoken = { done: "done", current: "under way", failed: "failed", todo: "to do" } as const;

/** The confirmation: what happens, then Cancel and Make Control Plane. A refusal stays in it. */
function ControlMoveDialog({ store, machine, address, frozenSeconds, close }: {
  store: Store; machine: ControlMachine; address: string | undefined; frozenSeconds: number | undefined; close: () => void;
}) {
  const [starting, setStarting] = useState(false);
  const [refused, setRefused] = useState<string | null>(null);
  const start = async () => {
    setStarting(true);
    try {
      const started = await store.link.call("control/move", { machine: machine.id });
      takeMove(started.move);
      close();
    } catch (error) {
      setRefused(failedWords(error));
    } finally {
      setStarting(false);
    }
  };
  return (
    <Modal label={question(machine.name)} close={close}>
      <h2>{question(machine.name)}</h2>
      <ul class="control-move-happens">{happens(machine.name, address, location.host, frozenSeconds).map((line) => <li key={line}>{line}</li>)}</ul>
      {refused && <p class="failure small" role="alert">{refused}</p>}
      <div class="sheet-actions">
        <button type="button" onClick={close}>Cancel</button>
        <button type="button" class="prominent" disabled={starting} onClick={() => void start()}>{words.confirm}</button>
      </div>
    </Modal>
  );
}

/** Across the page while the control plane moves, and for a day after. Show Machine opens its page. */
export function ControlMoveStrip({ store }: { store: Store }) {
  if (!showsControlMove(store)) return null;
  const move = stripMove();
  if (!move) return null;
  const busy = running(move);
  const heard = movesHeardAt.value;
  return (
    <Strip tone={move.phase === "failed" ? "warning" : "neutral"} name="control-move-strip" busy={busy}
      line={moveLine(move)} detail={forwardingLine(move)}
      accessory={busy && heard !== null ? <LastUpdate at={heard} /> : null}
      primary={{ label: "Show Machine", title: "Its page, with the move's steps", run: () => go({ nav: "machines", section: `machine-${move.to}` }) }}
      second={busy ? null : { label: "Close", title: "Hide this until the next move", run: () => closeMove(move) }} />
  );
}
