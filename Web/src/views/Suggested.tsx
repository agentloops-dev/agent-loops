// New Session's Suggested section (#5461, epic #5457): the epics with no lead, from work/suggest-leads (#5458), at most 3.
// A tap fills the form as Start lead does and starts nothing; Dismiss hides an epic until its progress changes and reads the list again (#5731); Hide, the section.
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import type { WorkSuggestLeads } from "../protocol/generated";
import type { Store } from "../model/store";
import { hostHas } from "../model/capabilities";
import { flagOn } from "../model/featureFlags";
import { CallFailed, describe, methodNotFound } from "../model/errors";
import { keepSuggestedHidden, moreWords, suggestedHidden, suggestionDetail, unreadWords } from "../model/suggestedLeads";
import { go } from "../route";
import { Icon } from "./Icon";
import { LastUpdate } from "./LastUpdate";
import { offerStart } from "./NewAgent";

const storage = typeof localStorage === "undefined" ? undefined : localStorage;

export function Suggested({ store, host, down }: { store: Store; host: string; down: boolean }) {
  const hidden = useSignal(suggestedHidden(storage));
  const got = useSignal<{ list: WorkSuggestLeads; at: number } | undefined>(undefined);
  const said = useSignal<string | null>(null);
  const failed = useSignal<string | null>(null);
  // One more each Dismiss: the list is read again, so the next epic comes in at its place (#5731).
  const reads = useSignal(0);
  const asked = !down && hostHas(store.hosts.value, host, "work.suggest-leads/1");
  // Read when the form opens; a read that fails keeps the last one, and the line says how old it is (#5139).
  // A first read that fails says why in one line (#5790); a host that does not know the method draws nothing.
  useEffect(() => {
    if (!asked || hidden.value) return;
    let current = true;
    store.link.call("work/suggest-leads", {}, host).then(
      (list) => { if (current) { got.value = { list, at: Date.now() }; failed.value = null; } },
      (e) => {
        if (!current || (e instanceof CallFailed && e.code === methodNotFound)) return;
        failed.value = `Could not read: ${describe(e)}`;
      },
    );
    return () => { current = false; };
  }, [asked, host, hidden.value, reads.value]);
  if (!asked) return null;
  const toggle = (value: boolean) => { hidden.value = value; keepSuggestedHidden(storage, value); };
  if (hidden.value) {
    return <button type="button" class="link small suggested-show" onClick={() => toggle(false)}>Show suggestions</button>;
  }
  const list = got.value?.list;
  // A read that failed, or a forge it could not read and no rows: one line says why (#5790).
  const why = list ? unreadWords(list) : failed.value;
  if (why) {
    return (
      <section class="suggested" aria-label="Suggested">
        <div class="suggested-head"><h2>Suggested</h2></div>
        <p class="failure small">{why}</p>
      </section>
    );
  }
  if (!list || list.suggestions.length === 0) return null;
  const lead = (i: number) => {
    const s = list.suggestions[i]!;
    if (!s.folder) { said.value = "No folder on this machine is on this epic's repo."; return; }
    said.value = offerStart(store, host, s.folder, s.start);
  };
  const dismiss = async (i: number) => {
    const s = list.suggestions[i]!;
    try {
      await store.link.call("work/dismiss-lead", { node: s.node }, host);
      got.value = { at: got.value!.at, list: { ...list, suggestions: list.suggestions.filter((_, j) => j !== i) } };
      reads.value++;
    } catch (e) { said.value = describe(e); }
  };
  const more = moreWords(list.more);
  return (
    <section class="suggested" aria-label="Suggested">
      <div class="suggested-head">
        <h2>Suggested</h2>
        <span class="suggested-end"><LastUpdate at={got.value!.at} />
          <button type="button" class="link small" onClick={() => toggle(true)} title="Hide the suggestions">Hide</button></span>
      </div>
      <ul>
        {list.suggestions.map((s, i) => (
          <li key={s.node.ref}>
            <button type="button" class="suggested-row" onClick={() => lead(i)}
              title="Fill the form to lead this epic; nothing starts until Start">
              <span class="suggested-title">{s.title}</span>
              <span class="quiet small">{suggestionDetail(s)}</span>
            </button>
            <button type="button" class="suggested-dismiss" aria-label={`Dismiss ${s.title}`}
              title="Dismiss: hide this epic until it changes" onClick={() => void dismiss(i)}><Icon name="x" /></button>
          </li>
        ))}
      </ul>
      {more && flagOn("work") && <a class="small" href="#/work/timeline" onClick={(e) => { e.preventDefault(); go({ nav: "work", section: "timeline" }); }}>{more}</a>}
      {said.value && <p class="failure small">{said.value}</p>}
    </section>
  );
}
