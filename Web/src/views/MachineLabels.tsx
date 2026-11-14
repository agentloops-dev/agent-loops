// A machine page's Labels section (#5284), as the window's MachineLabelsSection: the provider's labels as they are, a
// person's with a button to take each off, and a field to add one. Workflows' Runs on and the Machines filters use them.
import { useState } from "preact/hooks";
import type { Store } from "../model/store";
import type { ControlMachine } from "../protocol/generated";
import { machineLabelProblem, machineLabelsHint, ownMachineLabels, parseMachineLabels } from "../model/machineLabels";

export function MachineLabels({ store, host, machine }: { store: Store; host: string; machine: ControlMachine }) {
  const [typed, setTyped] = useState("");
  const [problem, setProblem] = useState<string | null>(null);
  const own = ownMachineLabels(machine);
  const add = () => {
    const added = parseMachineLabels(typed);
    if (added.length === 0) return;
    const bad = added.map(machineLabelProblem).find((p) => p !== null);
    if (bad) return setProblem(bad);
    setProblem(null);
    const have = new Set(own.map((l) => l.toLowerCase()));
    void store.setMachineLabels(host, [...own, ...added.filter((l) => !have.has(l.toLowerCase()))]).then((ok) => { if (ok) setTyped(""); });
  };
  return (
    <section class="machine-labels" aria-labelledby="machine-labels">
      <h3 class="section-head" id="machine-labels">Labels</h3>
      {(machine.labels ?? []).length === 0 && <p class="hint">No labels.</p>}
      <ul class="plain">
        {(machine.autoLabels ?? []).map((label) => <li key={label} class="quiet" title="Set by the provider">{label}</li>)}
        {own.map((label) => (
          <li key={label}>
            {label}{" "}
            <button type="button" class="link small" aria-label={`Remove ${label}`}
              onClick={() => void store.setMachineLabels(host, own.filter((l) => l !== label))}>Remove</button>
          </li>
        ))}
      </ul>
      <form onSubmit={(e) => { e.preventDefault(); add(); }}>
        <input type="text" aria-label="Add a label" placeholder="Add a label" value={typed} autocapitalize="off" autocomplete="off"
          onInput={(e) => setTyped(e.currentTarget.value)} />
      </form>
      {problem && <p class="failure small">{problem}</p>}
      <p class="quiet small">{machineLabelsHint}</p>
    </section>
  );
}
