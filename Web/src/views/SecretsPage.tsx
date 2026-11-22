// Status ▸ Secrets (#5257, specs/5254-secrets-accounts-environment/wireframes.md sections 2, 2a and 5), as the window's
// SecretsPageView (Shared/UI/UserScope/SecretsPageView.swift): every secret of the person's on one page, at #/secrets, and
// one selected at #/secrets/<name>. A search (never a value), chips with counts, a Project menu and a sort over a list with
// the Needs you group on top, and the selected secret's detail beside it; under 720 px the list is alone, and a press
// opens the detail with a back arrow. It replaces the read-only Status list (#4161) and Settings ▸ You ▸ Secrets.
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import type { LocalSecrets, OutsideAccount, SessionSecrets, UserSecretRow } from "../protocol/generated";
import type { Store } from "../model/store";
import { savedByLine } from "../model/secretOffers";
import { go, route } from "../route";
import { describe } from "../model/errors";
import { localDidNotLoad, localSecretWords } from "../model/localSecrets";
import { Capability } from "../protocol/capabilities.generated";
import { about, cardKind, isSecretName, secretsLeft } from "../model/userScope";
import { askBeforeUseHint, askBeforeUseWords } from "../model/askBeforeUse";
import { machineNames, onlyMachine, onlyMachineLine } from "../model/secretInfo";
import { removeQuestion, sessionSecretWords } from "../model/sessionSecrets";
import { accountID } from "../model/accountsPage";
import {
  ageLine, type Chip, chips, chipTitles, counts as chipCounts, groups as groupsOf, type Item, items as itemsOf, kindLabel, noMatch,
  projectsOf, recentLine, rowHint, shortAge, type Sort, sorts, sortTitles, subtitle, w, whereLine,
} from "../model/secretsPage";
import { CardSheet, SecretSheet, type SecretShow, sentence, setSecret, ShownValue, useSecretShow, useUserScope } from "./UserScope";
import { SessionSecretSheet } from "./settings/SessionSecrets";
import { useSettingsMachine } from "./settings/SettingsMachine";
import { useUserScopeStatus } from "./StatusSecrets";
import { SecretInfoLine } from "./SecretInfoLine";
import { SecretDescriptionField } from "./SecretDescription";
import { AskBeforeUseCheck } from "./AskBeforeUse";
import { LastUpdate } from "./LastUpdate";
import { ask } from "./MessageKinds";
import { Icon } from "./Icon";

/** The route's item for an item: a secret of the person's by its name, the others by their id. */
export function itemRef(item: Pick<Item, "id" | "place" | "name">): string {
  return item.place === "yours" ? item.name : item.id;
}

/** The item id a route's item names. */
export function itemID(ref: string | undefined): string | null {
  if (!ref) return null;
  return ref.startsWith("session/") || ref.startsWith("machine/") ? ref : `yours/${ref}`;
}

function open(item: Item | null): void {
  go(item ? { nav: "status", section: "secrets", item: itemRef(item) } : { nav: "status", section: "secrets" });
}

/** Under 720 px the list is alone (wireframes §2). The Accounts page (#5258) and the Environment page (#5260) use it too. */
export function useNarrow(): boolean {
  const query = "(max-width: 719px)";
  const narrow = useSignal(globalThis.matchMedia?.(query).matches ?? false);
  useEffect(() => {
    const list = globalThis.matchMedia?.(query);
    if (!list) return;
    const changed = () => { narrow.value = list.matches; };
    list.addEventListener("change", changed);
    return () => list.removeEventListener("change", changed);
  }, []);
  return narrow.value;
}

type Adding = "secret" | "card" | "session" | null;

export function SecretsPage({ store, down }: { store: Store; down: boolean }) {
  const { names, problem, load, readAt } = useUserScope(store, down);
  useUserScopeStatus(store, !down);
  const { host, name: hostName, picker } = useSettingsMachine(store);
  const sessions = useSignal<SessionSecrets | null>(null);
  const local = useSignal<LocalSecrets | null>(null);
  const localProblem = useSignal<string | null>(null);
  const search = useSignal("");
  const chip = useSignal<Chip>("all");
  const project = useSignal<string | null>(null);
  const sort = useSignal<Sort>("lastUsed");
  const adding = useSignal<Adding>(null);
  const replacing = useSignal<UserSecretRow | null>(null);
  const searchField = useRef<HTMLInputElement>(null);
  const narrow = useNarrow();
  const changes = store.userScopeChanges.value;

  const loadHost = async () => {
    if (!host) return;
    sessions.value = await store.link.call("secrets/sessionList", {}, host).catch(() => null);
    localProblem.value = null;
    try {
      local.value = await store.link.call("secrets/localList", {}, host);
    } catch (error) {
      // An empty list would hide what agents there still use (#6335): say why, unless the host is too old for it.
      local.value = null;
      if (store.has(host, Capability.hostLocalSecrets1)) localProblem.value = localDidNotLoad(describe(error));
    }
  };
  useEffect(() => { if (!down) void loadHost(); }, [host, down, changes]);
  // ⌘F (Ctrl+F) puts the cursor in the search, as the window's (wireframes §2).
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f") {
        e.preventDefault();
        searchField.current?.focus();
      }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, []);

  const rows = names.value?.secrets ?? [];
  const show = useSecretShow(store, rows);
  const hostNames = machineNames(store.hosts.value);
  const accounts = store.userScopeStatus.value?.accounts ?? [];
  const isMac = host !== undefined && host === store.macHostID.value;
  const all = itemsOf({
    yours: rows, sessions: sessions.value?.secrets ?? [], local: local.value?.secrets ?? [], accounts, machineNames: hostNames,
    hostName: isMac ? "this Mac" : hostName ?? "the machine", noCopy: show.noCopy.value, clientName: "this browser",
  });
  const counts = chipCounts(all, search.value, project.value);
  const groups = groupsOf(all, search.value, chip.value, project.value, sort.value);
  const shown = groups.flatMap((g) => g.items);
  const selectedID = itemID(route.value.section === "secrets" ? route.value.item : undefined);
  const selected = all.find((i) => i.id === selectedID) ?? null;
  const projects = projectsOf(all);
  const only = onlyMachine(rows);
  const left = store.secretsLeft.value;
  const reloadAll = async () => { await load(); await loadHost(); void store.loadUserScopeStatus(); };

  const move = (step: number) => {
    if (shown.length === 0) return;
    const at = selected ? shown.findIndex((i) => i.id === selected.id) : -1;
    const next = shown[Math.max(0, Math.min(shown.length - 1, at + step))];
    if (next) open(next);
  };
  const listKeys = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
  };

  const listPane = (
    <div class="secrets-list" role="listbox" aria-label="Secrets" tabIndex={0} onKeyDown={listKeys}>
      {problem.value && <p class="hint failure">{problem.value}</p>}
      {localProblem.value && <p class="hint failure">{localProblem.value}</p>}
      {local.value?.keyProblem && <p class="hint attention"><Icon name="triangle-alert" />{" "}{local.value.keyProblem}</p>}
      {left && (
        <div class="banner attention" role="status">
          <p>{secretsLeft(left)}</p>
          <button type="button" onClick={() => { store.secretsLeft.value = null; }}>OK</button>
        </div>
      )}
      {only && <p class="hint attention"><Icon name="triangle-alert" />{" "}{onlyMachineLine(only, hostNames)}</p>}
      {all.length === 0 && names.value && <p class="hint">{w.none}</p>}
      {all.length > 0 && groups.length === 0 && search.value && (
        <p class="hint">{noMatch(search.value)}{" "}
          <button type="button" class="link" onClick={() => { search.value = ""; }}>{w.clearSearch}</button>
        </p>
      )}
      {groups.map((group) => (
        <div key={group.id} class="secrets-group" role="group" aria-label={group.title}>
          <h3 class="secrets-group-head">{group.title} · {group.items.length}</h3>
          {group.items.map((item) => (
            <button key={item.id} type="button" role="option" aria-selected={selected?.id === item.id}
              class={`secrets-row${selected?.id === item.id ? " on" : ""}${item.needs ? " needs" : ""}`} onClick={() => open(item)}>
              <span class="secrets-row-name">
                {item.needs && <Icon name="triangle-alert" />}
                <code>{item.name}</code>
              </span>
              <code class="secrets-row-hint" aria-label={item.isCard ? "Card" : item.hint ? `Ends in ${item.hint}` : "Value hidden"}>{rowHint(item)}</code>
              <span class="secrets-row-age">{shortAge(item) ?? ""}</span>
              <span class={`secrets-row-sub${item.needs ? " warning" : ""}`}>{subtitle(item)}</span>
            </button>
          ))}
        </div>
      ))}
      <p class="hint small">{all.length === 1 ? "1 secret" : `${all.length} secrets`}</p>
    </div>
  );

  const detailPane = selected
    ? <SecretDetail key={selected.id} store={store} item={selected} down={down} show={show} host={host}
        offersAsk={selected.place === "yours" || (!!host && store.has(host, Capability.hostSecretApproval1))}
        accounts={accounts.filter((a) => a.secret === selected.name || a.card === selected.name)}
        replace={(row) => { replacing.value = row; }} changed={() => void reloadAll()} />
    : (
      <div class="secrets-empty">
        <p class="hint">{w.selectOne}</p>
        <p class="hint small">{`${counts.yours} yours · ${counts.session} for a session · ${counts.machine} on a machine`}</p>
      </div>
    );

  return (
    <div class={`secrets-page${narrow ? " narrow" : ""}`}>
      <div class="secrets-tools" role="search">
        <input ref={searchField} type="search" aria-label="Search secrets" placeholder={w.searchPrompt} value={search.value}
          onInput={(e) => { search.value = (e.target as HTMLInputElement).value; }}
          onKeyDown={(e) => { if (e.key === "Escape") search.value = ""; else if (e.key === "ArrowDown") { e.preventDefault(); move(1); } }} />
        <details class="secrets-add">
          <summary class="button prominent" aria-disabled={down}>+ {w.add}</summary>
          <div class="menu" role="menu">
            <button type="button" role="menuitem" disabled={down} onClick={(e) => { closeMenu(e); adding.value = "secret"; }}>{w.addSecret}</button>
            <button type="button" role="menuitem" disabled={down} onClick={(e) => { closeMenu(e); adding.value = "card"; }}>{w.addCard}</button>
            <button type="button" role="menuitem" disabled={down || !host} onClick={(e) => { closeMenu(e); adding.value = "session"; }}>{w.addForSession}</button>
          </div>
        </details>
        {readAt.value !== undefined && <LastUpdate at={readAt.value} />}
      </div>
      <div class="secrets-chips machine-chips" role="group" aria-label="Filters">
        {chips.map((c) => (
          <button key={c} type="button" class={`chip${chip.value === c ? " on" : ""}${c === "needsYou" ? " attention" : ""}`}
            aria-pressed={chip.value === c} onClick={() => { chip.value = chip.value === c && c !== "all" ? "all" : c; }}>
            {c === "needsYou" && <><Icon name="triangle-alert" />{" "}</>}{chipTitles[c]} {counts[c]}
          </button>
        ))}
        {(projects.length > 0 || project.value !== null) && (
          <label class="select-label">Project
            <select value={project.value ?? ""} onChange={(e) => { project.value = (e.target as HTMLSelectElement).value || null; }}>
              <option value="">{w.projectAny}</option>
              {projects.map((p) => <option key={p} value={p}>{p}</option>)}
            </select>
          </label>
        )}
        <label class="select-label">{w.sortLabel}
          <select value={sort.value} onChange={(e) => { sort.value = (e.target as HTMLSelectElement).value as Sort; }}>
            {sorts.map((s) => <option key={s} value={s}>{sortTitles[s]}</option>)}
          </select>
        </label>
        {picker}
      </div>
      {narrow ? (
        selected ? (
          <div class="secrets-detail-narrow">
            <button type="button" class="link" onClick={() => open(null)}>‹ {w.title}</button>
            {detailPane}
          </div>
        ) : listPane
      ) : (
        <div class="secrets-split">
          {listPane}
          <div class="secrets-detail">{detailPane}</div>
        </div>
      )}
      {adding.value === "secret" && (
        <SecretSheet store={store} replacing={null} close={() => { adding.value = null; void reloadAll(); }}
          saved={(name) => go({ nav: "status", section: "secrets", item: name })} />
      )}
      {adding.value === "card" && (
        <CardSheet store={store} replacing={null} close={() => { adding.value = null; void reloadAll(); }}
          saved={(name) => go({ nav: "status", section: "secrets", item: name })} />
      )}
      {adding.value === "session" && host && (
        <SessionSecretSheet store={store} host={host} offersAsk={store.has(host, Capability.hostSecretApproval1)}
          close={() => { adding.value = null; }}
          saved={(after, agentID, name) => {
            sessions.value = after;
            go({ nav: "status", section: "secrets", item: `session/${agentID}/${name}` });
          }} />
      )}
      {replacing.value && (replacing.value.kind === cardKind
        ? <CardSheet store={store} replacing={replacing.value} close={() => { replacing.value = null; void reloadAll(); }} />
        : <SecretSheet store={store} replacing={replacing.value} close={() => { replacing.value = null; void reloadAll(); }} />)}
    </div>
  );
}

/** Closes the + Add menu once an item is chosen. */
function closeMenu(e: Event): void {
  (e.currentTarget as HTMLElement).closest("details")?.removeAttribute("open");
}

/** The detail of one secret (wireframes §2): the value line, the kind, where it is kept, Ask before use, the projects,
 * Used by, the age line, Recent use, and Edit, Replace Value… and Remove…. Edit turns the description and Ask before use
 * into fields in place, with the value: both are sealed with it (#4016, #4167). */
function SecretDetail({ store, item, down, show, host, offersAsk, accounts, replace, changed }: {
  store: Store; item: Item; down: boolean; show: SecretShow; host: string | undefined; offersAsk: boolean;
  accounts: readonly OutsideAccount[]; replace: (row: UserSecretRow) => void; changed: () => void;
}) {
  const editing = useSignal(false);
  const description = useSignal(item.description ?? "");
  const askOn = useSignal(item.asksBeforeUse);
  const value = useSignal("");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const row = item.row ?? null;
  const shown = row && show.shown.value?.name === row.name ? show.shown.value : null;
  const copied = row && show.copied.value?.name === row.name ? show.copied.value : null;
  const off = down || (row !== null && (show.noCopy.value.has(row.name) || show.fetching.value === row.name));
  const title = (id: string) => Object.values(store.agents.value).flat().find((a) => a.id === id)?.title;

  const startEdit = (ask: boolean) => {
    description.value = item.description ?? "";
    askOn.value = ask;
    value.value = "";
    problem.value = null;
    editing.value = true;
  };
  const setAsk = async (on: boolean) => {
    if (item.place === "yours") {
      startEdit(on);
      return;
    }
    if (!host) return;
    try {
      await store.link.call("secrets/setNeedsApproval",
        item.place === "session" ? { name: item.name, agentID: item.agentID as never, needsApproval: on } : { name: item.name, needsApproval: on }, host);
      changed();
    } catch (error) {
      problem.value = describe(error);
    }
  };
  const save = async () => {
    if (!row) return;
    busy.value = true;
    try {
      await setSecret(store, row.name, value.value, row, askOn.value, description.value, row.kind);
      value.value = "";
      editing.value = false;
      changed();
    } catch (error) {
      problem.value = sentence(error);
    } finally {
      busy.value = false;
    }
  };
  const remove = async () => {
    if (item.place === "yours") {
      if (!await ask(`Remove ${item.name}? It is taken out of the sealed store on every machine of yours.`)) return;
      try {
        await store.link.call("user/removeSecret", { name: item.name as never });
      } catch (error) {
        problem.value = sentence(error);
        return;
      }
    } else if (item.place === "session" && item.session && host) {
      if (!await ask(`${removeQuestion(item.session)} ${sessionSecretWords.removeMessage}`)) return;
      try {
        await store.link.call("secrets/sessionRemove", { agentID: item.session.agentID, name: item.name }, host);
      } catch (error) {
        problem.value = describe(error);
        return;
      }
    } else if (item.place === "machine" && host) {
      if (!await ask(`Remove ${item.name}? ${localSecretWords.removeMessage}`)) return;
      try {
        await store.link.call("secrets/localRemove", { name: item.name }, host);
      } catch (error) {
        problem.value = describe(error);
        return;
      }
    } else return;
    open(null);
    changed();
  };

  return (
    <article class="secret-detail" aria-label={item.name}>
      <header class="secret-detail-head">
        <h2><code>{item.name}</code></h2>
        {row && show.can && (
          <span class="row-actions">
            <button type="button" disabled={off} aria-label={`${shown ? "Hide" : "Reveal"} ${row.name}`} onClick={() => void show.toggle(row)}>
              {show.fetching.value === row.name ? "…" : shown ? "Hide" : "Reveal"}
            </button>
            <button type="button" disabled={off} aria-label={`Copy ${row.name}`} onClick={() => void show.copy(row)}>
              {copied && copied.part === null ? "Copied ✓" : "Copy"}
            </button>
          </span>
        )}
      </header>
      {editing.value ? (
        <form class="secret-edit" onSubmit={(e) => { e.preventDefault(); void save(); }}>
          <SecretDescriptionField text={description} />
          <AskBeforeUseCheck on={askOn} />
          <input type="password" aria-label="Value" placeholder="Value" autocomplete="off" value={value.value}
            onInput={(e) => { value.value = (e.currentTarget as HTMLInputElement).value; }} />
          <p class="hint">{w.editNote}</p>
          {problem.value && <p class="failure" role="alert">{problem.value}</p>}
          <div class="sheet-actions">
            <button type="button" onClick={() => { editing.value = false; value.value = ""; }}>Cancel</button>
            <button type="submit" class="prominent" disabled={busy.value || !value.value || !isSecretName(item.name)}>Save</button>
          </div>
        </form>
      ) : (
        <>
          <p class="secret-about">{about(item.description)}</p>
          {row?.savedBy && <p class="hint">{savedByLine(row.savedBy)}</p>}
          <dl class="secret-facts">
            <dt>Value</dt>
            <dd>
              {shown && row ? <ShownValue row={row} shown={shown} show={show} /> : <code>{item.isCard ? "Card" : rowHint(item)}</code>}
              {row && show.noCopy.value.has(row.name) && <p class="hint attention">No copy for this browser yet.</p>}
              {row && show.problems.value[row.name] && <p class="failure" role="alert">{show.problems.value[row.name]}</p>}
            </dd>
            <dt>Kind</dt><dd>{kindLabel(item)}</dd>
            <dt>Where</dt><dd>{whereLine(item)}</dd>
            {offersAsk && (
              <>
                <dt>{askBeforeUseWords.toggle}</dt>
                <dd>
                  <label class="switch">
                    <input type="checkbox" role="switch" checked={item.asksBeforeUse} disabled={down}
                      aria-label={`${askBeforeUseWords.toggle}: ${item.name}`}
                      onChange={(e) => void setAsk((e.currentTarget as HTMLInputElement).checked)} />
                  </label>
                  <span class="hint small">{askBeforeUseHint(item.asksBeforeUse)}</span>
                </dd>
              </>
            )}
            {item.projects.length > 0 && <><dt>Projects</dt><dd>{item.projects.join(", ")}</dd></>}
            {accounts.length > 0 && (
              <>
                <dt>{w.usedBy}</dt>
                <dd>
                  {accounts.map((a) => (
                    <button key={`${a.service}#${a.address ?? ""}`} type="button" class="link"
                      onClick={() => go({ nav: "status", section: "accounts", item: accountID(a) })}>
                      {a.address ? `${a.service} (${a.address})` : a.service}
                    </button>
                  ))}
                </dd>
              </>
            )}
            {item.needs && <><dt>Needs you</dt><dd class="warning"><Icon name="triangle-alert" />{" "}{item.needs}</dd></>}
          </dl>
          <SecretInfoLine parts={ageLine(item)} class="hint small" />
          <h3 class="secrets-group-head">{w.recentUse}</h3>
          <p>{recentLine(item, new Date(), title) ?? w.noUse}</p>
          {problem.value && <p class="failure" role="alert">{problem.value}</p>}
          <div class="secret-actions">
            {item.place === "yours" && row && (
              <>
                <button type="button" disabled={down} onClick={() => replace(row)}>{w.replaceValue}</button>
                <button type="button" disabled={down} onClick={() => startEdit(item.asksBeforeUse)}>{w.edit}</button>
              </>
            )}
            <span class="spacer" />
            {(item.place !== "machine" || (!!host && store.has(host, Capability.hostLocalSecrets2))) && <button type="button" class="destructive" disabled={down} onClick={() => void remove()}>{w.remove}</button>}
          </div>
        </>
      )}
    </article>
  );
}
