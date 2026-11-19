// New Machine (#4634, #5354), as the window's NewMachineSheet with its choices (#5358): the provider (a picker with two
// or more), Start from (a warm pool of that provider and size with a ready machine, or a new machine, #5366), the
// provider's size, and Stop when idle. The name and every other field come from what New Machine would make (the options
// used last on the provider, else its first choices) and the next machine-N. Make is the confirmation, and closes the
// sheet: the Machines group's Making row takes over, and the machine's page opens once it is listed. A make the provider
// refuses opens it again on the same choices, with the reason. Projects to send are the Mac's only: Send to ‹machine›…
// is the Mac's (#1123).
import { useEffect, useState } from "preact/hooks";
import type { JSONValue } from "../protocol/generated";
import type { NewMachineAsk, Store } from "../model/store";
import { describe } from "../model/errors";
import { choiceLabel, createRequest, formFields, makers, withValues, type FormField } from "../model/machineFromRepository";
import { validMachineName } from "../model/addMachine";
import { defaultIdleMinutes, idleChoices } from "../model/machineProviders";
import { matchingPools, newMachineLine, startFromLine } from "../model/warmPools";
import { Modal } from "./Modal";

/** With the choices, the form shows only the provider's size: a field named size, else its first choice. */
function sizeField(fields: readonly FormField[]): FormField | undefined {
  return fields.find((f) => f.key.toLowerCase() === "size" || f.title.toLowerCase() === "size") ?? fields.find((f) => f.choices);
}

export function NewMachineSheet({ store, ask }: { store: Store; ask: NewMachineAsk }) {
  const providers = store.providers.list.value;
  const choices = makers(providers ?? []);
  const [provider, setProvider] = useState(ask.again?.provider ?? ask.provider ?? "");
  const [name, setName] = useState(ask.again?.name ?? "");
  const [fields, setFields] = useState<FormField[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState(ask.problem);
  const [idle, setIdle] = useState(ask.again?.stopWhenIdleMinutes ?? defaultIdleMinutes);
  const [fromPool, setFromPool] = useState(ask.again?.fromPool ?? true);
  useEffect(() => { if (providers === null) void store.providers.load(); }, []);
  // The person's warm pools, for Start from; a refused make that was not a claim asks no pool again.
  useEffect(() => { if (!ask.again || ask.again.fromPool) void store.providers.loadPools(); }, []);
  const close = () => { store.newMachineAsk.value = null; };
  // The provider's form and what New Machine would make, each time another provider is chosen. The first time, a
  // refused make's own choices are kept.
  const [kept, setKept] = useState(ask.again);
  useEffect(() => {
    let live = true;
    setLoading(true);
    void store.providers.options(provider || undefined)
      .then((o) => {
        if (!live) return;
        if (o.id) setProvider(o.id);
        const form = formFields(o.schema);
        setFields(withValues(form, kept ? kept.options : o.values));
        if (!kept && (!name || name.startsWith("machine-")) && o.name) setName(o.name);
        setKept(undefined);
      })
      .catch((error: unknown) => { if (live) { setFields([]); setProblem(describe(error)); } })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [provider]);
  const size = sizeField(fields);
  const pools = store.providers.poolsHere.value ? store.providers.pools.value : [];
  const pool = loading ? undefined : matchingPools(pools, provider, size?.value || undefined)[0];
  const hasReady = (pool?.ready ?? 0) > 0;
  const claims = fromPool && !!pool && hasReady;
  const named = name.trim();
  const ready = !!provider && !loading && validMachineName(named);
  const cost = fields.map((f) => f.costs?.[f.value]).filter((c): c is string => !!c).join(" · ");
  const make = () => {
    if (!ready) return;
    const request = createRequest(provider, named, null, fields);
    close();
    void store.makeMachine({ provider, name: named, options: (request.options ?? {}) as Record<string, JSONValue>,
      stopWhenIdleMinutes: idle, ...(claims ? { fromPool: true } : {}) });
  };
  const set = (key: string, value: string) => setFields(fields.map((g) => (g.key === key ? { ...g, value } : g)));
  const title = choices.length > 1 || !choices[0] ? "New Machine" : `New Machine on ${choices[0].name}`;
  return (
    <Modal label={title} close={close}>
      <form class="sheet-body connect-sheet provider-sheet" onSubmit={(e) => { e.preventDefault(); make(); }}>
        <h2>{title}</h2>
        <div class="token-form">
          {choices.length > 1 && (
            <label>Provider
              <select value={provider} onChange={(e) => setProvider((e.target as HTMLSelectElement).value)}>
                {choices.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </label>
          )}
          {pool && (
            <fieldset class="start-from">
              <legend>Start from</legend>
              <label><input type="radio" name="start-from" checked={claims} disabled={!hasReady}
                onChange={() => setFromPool(true)} />{" "}Warm pool “{pool.name}” · {startFromLine(pool)}</label>
              <label><input type="radio" name="start-from" checked={!claims} onChange={() => setFromPool(false)} />
                {" "}A new machine · {newMachineLine}</label>
            </fieldset>
          )}
          {loading ? <p class="small quiet">Reading the provider’s choices…</p> : size && (
            <label key={size.key} title={size.description}>{size.title}
              {size.choices
                ? <select value={size.value} onChange={(e) => set(size.key, (e.target as HTMLSelectElement).value)}>
                    {size.choices.map((c) => <option key={c} value={c}>{choiceLabel(size, c)}</option>)}
                  </select>
                : <input type="text" value={size.value} autocomplete="off" spellcheck={false}
                    onInput={(e) => set(size.key, (e.target as HTMLInputElement).value)} />}
            </label>
          )}
          {!loading && pools.some((p) => p.provider === provider) && (
            <p class="small quiet">A pool has one size: choosing another size starts a new machine.</p>
          )}
          <label>Stop when idle
            <select value={String(idle)} onChange={(e) => setIdle(Number((e.target as HTMLSelectElement).value))}>
              {idleChoices.map((c) => <option key={c.minutes} value={String(c.minutes)}>{c.label}</option>)}
            </select>
          </label>
        </div>
        {problem && <p class="small failure" role="alert">{problem}</p>}
        <p class="small quiet">It joins as a machine by itself once it has started. The provider charges for it from when it is made.</p>
        <div class="sheet-actions">
          {cost && <span class="small quiet">{cost}</span>}
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={!ready}>Make</button>
        </div>
      </form>
    </Modal>
  );
}
