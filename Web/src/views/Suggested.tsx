// New Session's Suggested section (#5461, epic #5457): the epics with no lead, from work/suggest-leads (#5458), at most 3.
// A tap fills the form as Start lead does and starts nothing; Dismiss hides an epic until it changes; Hide, the section.
import { useSignal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import type { WorkSuggestLeads } from "../protocol/generated";
import type { Store } from "../model/store";
import { hostHas } from "../model/capabilities";
import { describe } from "../model/errors";
import { keepSuggestedHidden, moreWords, suggestedHidden, suggestionDetail } from "../model/suggestedLeads";
import { go } from "../route";
import { LastUpdate } from "./LastUpdate";
import { offerStart } from "./NewAgent";

const storage = typeof localStorage === "undefined" ? undefined : localStorage;

export function Suggested({ store, host, down }: { store: Store; host: string; down: boolean }) {
  const hidden = useSignal(suggestedHidden(storage));
  const got = useSignal<{ list: WorkSuggestLeads; at: number } | undefined>(undefined);
  const said = useSignal<string | null>(null);
  const asked = !down && hostHas(store.hosts.value, host, "work.suggest-leads/1");
  // Read when the form opens; a read that fails keeps the last one, and the line says how old it is (#5139).
  useEffect(() => {
    if (!asked || hidden.value) return;
    let current = true;
    store.link.call("work/suggest-leads", {}, host).then(
      (list) => { if (current) got.value = { list, at: Date.now() }; },
      () => { /* nothing to suggest is not a failure of the form */ },
    );
    return () => { current = false; };
  }, [asked, host, hidden.value]);
  if (!asked) return null;
  const toggle = (value: boolean) => { hidden.value = value; keepSuggestedHidden(storage, value); };
  if (hidden.value) {
    return <button type="button" class="link small suggested-show" onClick={() => toggle(false)}>Show suggestions</button>;
  }
  const list = got.value?.list;
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
            <button type="button" class="link small suggested-dismiss" aria-label={`Dismiss ${s.title}`}
              title="Hide this epic until it changes" onClick={() => void dismiss(i)}>Dismiss</button>
          </li>
        ))}
      </ul>
      {more && <a class="small" href="#/work/features" onClick={(e) => { e.preventDefault(); go({ nav: "work", section: "features" }); }}>{more}</a>}
      {said.value && <p class="failure small">{said.value}</p>}
    </section>
  );
}
