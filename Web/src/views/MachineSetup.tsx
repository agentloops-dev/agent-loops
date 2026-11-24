// A machine's set-up as steps on its page (#5354, epic #5353, boards 2 to 4), as the window's MachineSetupSection: a
// mark, the name, one line and the time for each of the eight steps. A failed step stops the run, says why, and offers
// Try Again and Delete Machine…. Step 7 has a card for each runtime still to sign in, which opens the sign-in panel.
// After 24 hours ready the steps fold into one line. Read from the control plane's row, which it pushes as the machine
// changes (`ControlMachine.setup`); the page says when it last heard (#5139).
import { useEffect, useState } from "preact/hooks";
import type { ControlMachine, MachineSetup, MachineSetupState } from "../protocol/generated";
import type { Store } from "../model/store";
import { failedStep, foldedLine, isReady, stepLine, stepName, stepStateWords, stepTime } from "../model/machineSetup";
import { deleteQuestion } from "../model/machineRow";
import { offersMachineSignIn } from "../model/runtimes";
import { Capability } from "../protocol/capabilities.generated";
import { needsSignIn } from "./OfflineStrip";
import { signInRuntime } from "./RuntimeSignInDialog";
import { Icon, type IconName } from "./Icon";
import { ask } from "./MessageKinds";

export const marks: Record<MachineSetupState, IconName> = {
  done: "circle-check", running: "refresh-cw", "needs-you": "user-x", failed: "circle-x", waiting: "circle",
};

/** A second's tick while a step runs, so its time moves. */
export function useTick(on: boolean): Date {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    if (!on) return;
    const timer = setInterval(() => setNow(new Date()), 1_000);
    return () => clearInterval(timer);
  }, [on]);
  return now;
}

export function MachineSetupSection({ store, row, setup }: { store: Store; row: ControlMachine; setup: MachineSetup }) {
  const now = useTick(!isReady(setup) && !failedStep(setup));
  const folded = foldedLine(setup, now);
  const note = store.machinePoolNotes.value[row.name];
  if (folded) {
    return (
      <section aria-labelledby="machine-setup">
        <h3 class="section-head" id="machine-setup">Agents</h3>
        <p class="hint">{folded}</p>
      </section>
    );
  }
  const failed = failedStep(setup);
  const remove = async () => {
    if (await ask(deleteQuestion(row, 0))) void store.deleteHost(row.id);
  };
  return (
    <section class="machine-setup" aria-labelledby="machine-setup">
      <h3 class="section-head" id="machine-setup">Set-up</h3>
      <ol class="setup-steps">
        {setup.steps.map((step) => (
          <li key={step.id} class={`setup-step ${step.state}`}>
            <span class="setup-mark"><Icon name={marks[step.state]} title={stepStateWords[step.state]} /></span>
            <span class="setup-words">
              <span>{stepName(step)}</span>
              <span class="small quiet setup-line">{stepLine(step, now)}</span>
            </span>
            <span class="small quiet setup-time">{stepTime(step, now)}</span>
            {step.id === "signIn" && (step.state === "running" || step.state === "needs-you") && (
              <SignInCards store={store} host={row.id} name={row.name} />
            )}
          </li>
        ))}
      </ol>
      {failed && (
        <>
          <p class="failure" role="alert">{failed.error ?? `${stepName(failed)} failed.`}</p>
          <div class="row-actions">
            <button type="button" onClick={() => void store.retrySetup(row.id)}>Try Again</button>
            <button type="button" class="destructive" onClick={() => void remove()}>Delete Machine…</button>
          </div>
        </>
      )}
      {note && <p class="hint">{note}</p>}
    </section>
  );
}

/** Under step 7 (board 2 note 4): a card for each runtime on the machine still to sign in, as the window's
 * MachineSignInCards. Its Sign In… opens the one sign-in panel, with the runtime's code and page. A key the person has
 * in Secrets is written on the machine by the Mac, as the window does it, so no card shows for that runtime. */
function SignInCards({ store, host, name }: { store: Store; host: string; name: string }) {
  const waiting = (store.runtimes.value[host] ?? []).filter(needsSignIn);
  const signs = store.has(host, Capability.runtimesSignIn1);
  useEffect(() => {
    // The step changes the runtimes; read them again while it runs.
    void store.loadRuntimes(host);
    const timer = setInterval(() => void store.loadRuntimes(host), 5_000);
    return () => clearInterval(timer);
  }, [store, host]);
  if (waiting.length === 0) return null;
  return (
    <ul class="setup-sign-in">
      {waiting.map((status) => (
        <li key={status.runtime.id}>
          <span class="strong">Sign in to {status.runtime.name}</span>
          {signs && offersMachineSignIn(status)
            ? <button type="button" onClick={() => signInRuntime(store, { host, runtimeID: status.runtime.id, runtimeName: status.runtime.name, machine: name })}>
                Sign In…</button>
            : <span class="small quiet">{status.runtime.name} signs in on the machine from a terminal.</span>}
        </li>
      ))}
    </ul>
  );
}
