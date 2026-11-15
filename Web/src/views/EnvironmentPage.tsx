// Status ▸ Environment (#5260, specs/5254-secrets-accounts-environment/wireframes.md sections 4, 4a, 4b, 4c and 5), as the
// window's EnvironmentPageView: the plain variables sessions get when their agent starts, at #/environment, and one
// selected at #/environment/<item id>. Yours (~/.agents/env, every machine) and each repo's (.agents/env) on one machine,
// with a search, chips with counts, the Needs you group on top and the selected variable's detail beside the list; under
// 720 px the list is alone, as the Secrets page's. The rules are in ../model/environmentPage.ts.
import { useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import type { EnvFile, EnvList, EnvResolved, EnvResolveRequest, JSONValue, UserEnvironment } from "../protocol/generated";
import type { Store } from "../model/store";
import { go, route } from "../route";
import { describe } from "../model/errors";
import { Capability } from "../protocol/capabilities.generated";
import { sessionsForSecrets } from "../model/sessionSecrets";
import {
  alsoSet, asSessionTitle, cannotReach, type Chip, chips, chipTitles, counts as chipCounts, edit, groups as groupsOf, id as itemIDOf,
  type Item, items as itemsOf, layerLine, layerShort, noMatch, pickerFolders, refusal, refusedAsSecret, repoName, resolvedSource,
  subtitle, textOf, w, writeNote,
} from "../model/environmentPage";
import { useSettingsMachine } from "./settings/SettingsMachine";
import { useNarrow } from "./SecretsPage";
import { SecretSheet, sentence } from "./UserScope";
import { LastUpdate } from "./LastUpdate";
import { Modal } from "./Modal";
import { ask } from "./MessageKinds";
import { Icon } from "./Icon";

function open(item: Pick<Item, "id"> | string | null): void {
  const ref = typeof item === "string" ? item : item?.id;
  go(ref ? { nav: "status", section: "environment", item: ref } : { nav: "status", section: "environment" });
}

/** ~/.agents/env as the person's settings keep it, for an edit of one line: the environment part's text, or, before the
 * part was set, the text of the file this machine read. */
function yoursText(settings: UserEnvironment | null, files: readonly EnvFile[]): string {
  return settings?.text ?? textOf(files.find((f) => f.layer === "yours")?.variables ?? []);
}

/** Writes ~/.agents/env whole, through the control plane: it goes to every machine of the person's. */
async function setYours(store: Store, text: string): Promise<void> {
  const value: UserEnvironment = { text };
  await store.link.call("user/setSettings", { part: "environment", value: value as unknown as JSONValue });
}

/** Adds, changes (value) or removes (null) one variable, in the file its layer names. */
async function write(store: Store, host: string, layer: "yours" | "repo", folder: string | undefined, name: string, value: string | null,
  description: string | null, settings: UserEnvironment | null, files: readonly EnvFile[]): Promise<void> {
  if (layer === "yours") {
    await setYours(store, edit(yoursText(settings, files), name, value, description));
    return;
  }
  await store.link.call("env/write", {
    folder: folder ?? "", name, ...(value !== null ? { value } : {}), ...(description !== null ? { description } : {}),
  }, host);
}

export function EnvironmentPage({ store, down }: { store: Store; down: boolean }) {
  const { host, name: hostName, isMac, picker } = useSettingsMachine(store);
  const list = useSignal<EnvList | null>(null);
  const settings = useSignal<UserEnvironment | null>(null);
  const problem = useSignal<string | null>(null);
  const readAt = useSignal<number | undefined>(undefined);
  const search = useSignal("");
  const chip = useSignal<Chip>("all");
  const adding = useSignal(false);
  const asSession = useSignal(false);
  /** Save as a Secret Instead…: Add a Secret, here, with the name filled in. */
  const asSecret = useSignal<string | null>(null);
  const searchField = useRef<HTMLInputElement>(null);
  const narrow = useNarrow();
  const changes = store.userScopeChanges.value;
  const can = host !== undefined && store.has(host, Capability.env1);
  const online = host !== undefined && store.hostIsOnline(host);

  const load = async () => {
    if (!host || !can) { list.value = null; return; }
    try {
      const [files, user] = await Promise.all([
        store.link.call("env/list", {}, host),
        store.link.call("user/settings", {}).catch(() => null),
      ]);
      list.value = files;
      settings.value = (user?.environment as UserEnvironment | undefined) ?? null;
      readAt.value = Date.now();
      problem.value = null;
    } catch (error) {
      problem.value = describe(error);
    }
  };
  useEffect(() => { if (!down) void load(); }, [host, can, down, changes]);
  // A file changed on the machine (#5259): read again.
  useEffect(() => store.link.onNotification((method, _params, from) => {
    if (method === "env/changed" && from === host) void load();
  }), [host, can]);
  // ⌘F (Ctrl+F) puts the cursor in the search, as the Secrets page's.
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

  const files = list.value?.files ?? [];
  const all = itemsOf(files);
  const counts = chipCounts(all, search.value);
  const groups = groupsOf(files, search.value, chip.value);
  const shown = groups.flatMap((g) => g.items);
  const selectedID = route.value.section === "environment" ? route.value.item : undefined;
  const selected = all.find((i) => i.id === selectedID) ?? null;
  const yours = files.find((f) => f.layer === "yours");
  const machine = isMac ? "this Mac" : hostName ?? "";

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
  const keepForAll = async () => {
    try {
      await setYours(store, textOf(yours?.variables ?? []));
      await load();
    } catch (error) {
      problem.value = sentence(error);
    }
  };
  const discard = async () => {
    if (!host) return;
    try {
      await store.link.call("env/discard", {}, host);
      await load();
    } catch (error) {
      problem.value = describe(error);
    }
  };

  const reason = !host ? w.noMachine : !can ? w.tooOld : !online ? cannotReach(machine) : null;
  const listPane = (
    <div class="secrets-list" role="listbox" aria-label="Variables" tabIndex={0} onKeyDown={listKeys}>
      {reason && <p class="hint attention">{reason}</p>}
      {problem.value && <p class="hint failure">{problem.value}</p>}
      {yours?.changedByHand && (
        <div class="banner attention" role="status">
          <p>{w.changedByHand}</p>
          <button type="button" disabled={down} onClick={() => void keepForAll()}>{w.keepForAll}</button>
          <button type="button" disabled={down} onClick={() => void discard()}>{w.discard}</button>
        </div>
      )}
      {list.value && all.length === 0 && <p class="hint">{w.none}</p>}
      {all.length > 0 && groups.length === 0 && search.value && (
        <p class="hint">{noMatch(search.value)}{" "}
          <button type="button" class="link" onClick={() => { search.value = ""; }}>{w.clearSearch}</button>
        </p>
      )}
      {groups.map((group) => (
        <div key={group.id} class="secrets-group" role="group" aria-label={group.title}>
          <h3 class="secrets-group-head">
            {group.title}{group.file && <span class="env-group-file">{group.file}</span>}
            {group.uncommitted && <span class="env-mark">✎ {w.notCommitted}</span>}
          </h3>
          {group.problem && <p class="hint failure env-problem">{group.problem}</p>}
          {group.items.map((item) => (
            <button key={item.id} type="button" role="option" aria-selected={selected?.id === item.id}
              class={`secrets-row${selected?.id === item.id ? " on" : ""}${item.needs ? " needs" : ""}`} onClick={() => open(item)}>
              <span class="secrets-row-name">
                {item.needs && <Icon name="triangle-alert" />}
                <code>{item.name}</code>
              </span>
              <code class="secrets-row-hint env-row-value">{item.needs ? layerShort(item) : item.value}</code>
              <span />
              {item.needs && <span class="secrets-row-sub warning">{subtitle(item)}</span>}
            </button>
          ))}
        </div>
      ))}
      <p class="hint small">{all.length === 1 ? "1 variable" : `${all.length} variables`}</p>
    </div>
  );

  const detailPane = selected && host
    ? <VariableDetail key={selected.id} item={selected} all={all} down={down}
        write={(value, description) => write(store, host, selected.layer === "yours" ? "yours" : "repo", selected.folder, selected.name,
          value, description, settings.value, files)}
        asSecret={(name) => { asSecret.value = name; }} changed={() => void load()} />
    : <div class="secrets-empty"><p class="hint">{w.selectOne}</p></div>;

  return (
    <div class={`secrets-page${narrow ? " narrow" : ""}`}>
      <div class="secrets-tools" role="search">
        <input ref={searchField} type="search" aria-label="Search variables" placeholder={w.searchPrompt} value={search.value}
          onInput={(e) => { search.value = (e.target as HTMLInputElement).value; }}
          onKeyDown={(e) => { if (e.key === "Escape") search.value = ""; else if (e.key === "ArrowDown") { e.preventDefault(); move(1); } }} />
        <button type="button" class="prominent" disabled={down || !can} onClick={() => { adding.value = true; }}>+ {w.add}</button>
        {readAt.value !== undefined && <LastUpdate at={readAt.value} />}
      </div>
      <p class="hint small">{w.whereLine}</p>
      <div class="secrets-chips machine-chips" role="group" aria-label="Filters">
        {chips.map((c) => (
          <button key={c} type="button" class={`chip${chip.value === c ? " on" : ""}${c === "needsYou" ? " attention" : ""}`}
            aria-pressed={chip.value === c} onClick={() => { chip.value = chip.value === c && c !== "all" ? "all" : c; }}>
            {c === "needsYou" && <><Icon name="triangle-alert" />{" "}</>}{chipTitles[c]} {counts[c]}
          </button>
        ))}
        {picker}
        <span class="spacer" />
        <button type="button" disabled={!can || !online} onClick={() => { asSession.value = true; }}>{w.asSession}</button>
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
      {adding.value && host && (
        <AddVariableSheet files={files}
          known={(store.folders.value[host] ?? []).map((f) => f.name)}
          write={(layer, folder, name, value, description) => write(store, host, layer, folder, name, value, description, settings.value, files)}
          close={() => { adding.value = false; }}
          asSecret={(name) => { adding.value = false; asSecret.value = name; }}
          added={(ref) => { adding.value = false; void load(); open(ref); }} />
      )}
      {asSession.value && host && (
        <AsSessionSheet store={store} host={host} machine={isMac ? undefined : hostName}
          folders={pickerFolders(files, (store.folders.value[host] ?? []).map((f) => f.name))}
          close={() => { asSession.value = false; }} />
      )}
      {asSecret.value !== null && (
        <SecretSheet store={store} replacing={null} suggested={asSecret.value} close={() => { asSecret.value = null; }}
          saved={(name) => go({ nav: "status", section: "secrets", item: name })} />
      )}
    </div>
  );
}

/** The refusal line, and Save as a Secret Instead… when it says the value looks like a secret (wireframes §4b). */
function Refusal({ text, name, asSecret }: { text: string | null; name: string; asSecret: (name: string) => void }) {
  if (!text) return null;
  return (
    <div class="env-refusal" role="alert">
      <p class="failure"><Icon name="triangle-alert" />{" "}{text}</p>
      {refusedAsSecret(text) && <button type="button" onClick={() => asSecret(name.trim())}>{w.saveAsSecret}</button>}
    </div>
  );
}

/** One variable (wireframes §4a): its description, Value with Copy, Layer, Also set, Needs you, Not committed, when sessions
 * get it, and Edit (in place) and Remove…. */
function VariableDetail({ item, all, down, write, asSecret, changed }: {
  item: Item; all: readonly Item[]; down: boolean;
  write: (value: string | null, description: string | null) => Promise<void>; asSecret: (name: string) => void; changed: () => void;
}) {
  const editing = useSignal(false);
  const value = useSignal(item.value);
  const description = useSignal(item.description ?? "");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const copied = useSignal(false);
  const also = alsoSet(item, all);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(item.value);
      copied.value = true;
      setTimeout(() => { copied.value = false; }, 2000);
    } catch (error) {
      problem.value = describe(error);
    }
  };
  const save = async () => {
    const refused = refusal(item.name, value.value);
    if (refused) { problem.value = refused; return; }
    busy.value = true;
    try {
      await write(value.value, description.value.trim());
      editing.value = false;
      problem.value = null;
      changed();
    } catch (error) {
      problem.value = sentence(error);
    } finally {
      busy.value = false;
    }
  };
  const remove = async () => {
    const where = item.layer === "yours" ? "~/.agents/env on every machine of yours" : `.agents/env in ${item.repo ?? "the repo"}`;
    if (!await ask(`Remove ${item.name}? It is taken out of ${where}. ${w.nextStart}`)) return;
    try {
      await write(null, null);
    } catch (error) {
      problem.value = sentence(error);
      return;
    }
    open(null);
    changed();
  };

  return (
    <article class="secret-detail" aria-label={item.name}>
      <header class="secret-detail-head">
        <h2><code>{item.name}</code></h2>
        <span class="row-actions">
          <button type="button" aria-label={`Copy ${item.name}`} onClick={() => void copy()}>{copied.value ? "Copied ✓" : w.copy}</button>
        </span>
      </header>
      {editing.value ? (
        <form class="secret-edit" onSubmit={(e) => { e.preventDefault(); void save(); }}>
          <input type="text" aria-label="Value" placeholder="Value" autocomplete="off" spellcheck={false} value={value.value}
            onInput={(e) => { value.value = (e.currentTarget as HTMLInputElement).value; problem.value = null; }} />
          <input type="text" aria-label="Description" placeholder="Description" value={description.value}
            onInput={(e) => { description.value = (e.currentTarget as HTMLInputElement).value; }} />
          <p class="hint">{writeNote(item.layer)}</p>
          <Refusal text={problem.value ?? refusal(item.name, value.value)} name={item.name} asSecret={asSecret} />
          <div class="sheet-actions">
            <button type="button" onClick={() => { editing.value = false; problem.value = null; }}>Cancel</button>
            <button type="submit" class="prominent" disabled={busy.value || down || refusal(item.name, value.value) !== null}>Save</button>
          </div>
        </form>
      ) : (
        <>
          {item.description && <p class="secret-about">{item.description}</p>}
          <dl class="secret-facts">
            <dt>Value</dt><dd><code class="env-value">{item.value}</code></dd>
            <dt>Layer</dt><dd>{layerLine(item)}<code class="hint small">{item.path}</code></dd>
            {also.length > 0 && <><dt>Also set</dt><dd class="env-lines">{also.map((l) => <span key={l}>{l}</span>)}</dd></>}
            {item.needs && <><dt>Needs you</dt><dd class="warning"><Icon name="triangle-alert" />{" "}{item.needs}</dd></>}
            {item.layer === "repo" && item.uncommitted && <><dt>Committed</dt><dd>{w.notCommitted}</dd></>}
          </dl>
          <p class="hint small">{w.nextStart}</p>
          {problem.value && <p class="failure" role="alert">{problem.value}</p>}
          <div class="secret-actions">
            <button type="button" disabled={down} onClick={() => {
              value.value = item.value; description.value = item.description ?? ""; problem.value = null; editing.value = true;
            }}>{w.edit}</button>
            <span class="spacer" />
            <button type="button" class="destructive" disabled={down} onClick={() => void remove()}>{w.remove}</button>
          </div>
        </>
      )}
    </article>
  );
}

/** Add Variable (wireframes §4b): Name, Value, Description and Where (yours, or a repo's file on this machine). */
function AddVariableSheet({ files, known, write, close, asSecret, added }: {
  files: readonly EnvFile[]; known: readonly string[];
  write: (layer: "yours" | "repo", folder: string | undefined, name: string, value: string, description: string) => Promise<void>;
  close: () => void; asSecret: (name: string) => void; added: (ref: string) => void;
}) {
  const folders = pickerFolders(files, known);
  const name = useSignal("");
  const value = useSignal("");
  const description = useSignal("");
  const layer = useSignal<"yours" | "repo">("yours");
  const folder = useSignal(folders[0] ?? "");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const refused = refusal(name.value, value.value);
  const save = async () => {
    if (refused) return;
    const chosen = name.value.trim();
    busy.value = true;
    try {
      await write(layer.value, layer.value === "repo" ? folder.value : undefined, chosen, value.value, description.value.trim());
      added(itemIDOf(layer.value, layer.value === "repo" ? folder.value : undefined, chosen));
    } catch (error) {
      problem.value = sentence(error);
    } finally {
      busy.value = false;
    }
  };
  return (
    <Modal label={w.add} close={close}>
      <form method="dialog" class="sheet-body env-add" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <h2>{w.add}</h2>
        <label>Name
          <input type="text" placeholder="AWS_REGION" autofocus autocomplete="off" spellcheck={false} value={name.value}
            onInput={(e) => { name.value = (e.currentTarget as HTMLInputElement).value; problem.value = null; }} />
        </label>
        <label>Value
          <input type="text" placeholder="eu-west-2" autocomplete="off" spellcheck={false} value={value.value}
            onInput={(e) => { value.value = (e.currentTarget as HTMLInputElement).value; problem.value = null; }} />
        </label>
        <label>Description
          <input type="text" placeholder="The region for the deploy scripts." value={description.value}
            onInput={(e) => { description.value = (e.currentTarget as HTMLInputElement).value; }} />
        </label>
        <fieldset class="env-where">
          <legend>Where</legend>
          <label><input type="radio" name="env-where" checked={layer.value === "yours"} onChange={() => { layer.value = "yours"; }} />
            {" "}Yours <span class="hint small">{w.yoursWhere}</span></label>
          <label><input type="radio" name="env-where" checked={layer.value === "repo"} disabled={folders.length === 0}
            onChange={() => { layer.value = "repo"; }} />{" "}A repo <span class="hint small">.agents/env in</span>{" "}
            <select aria-label="Repo" value={folder.value} disabled={folders.length === 0}
              onChange={(e) => { folder.value = (e.currentTarget as HTMLSelectElement).value; layer.value = "repo"; }}>
              {folders.map((f) => <option key={f} value={f} title={f}>{repoName(f)}</option>)}
            </select>
          </label>
          <p class="hint small">{w.repoPublic}</p>
        </fieldset>
        <p class="sheet-note">{w.nextStart}</p>
        <Refusal text={problem.value ?? refused} name={name.value} asSecret={asSecret} />
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={busy.value || !name.value.trim() || refused !== null}>Add</button>
        </div>
      </form>
    </Modal>
  );
}

/** As a session sees it (wireframes §4c): a folder or a running session on this machine, and each name with its value and
 * the layer that wins. */
function AsSessionSheet({ store, host, machine, folders, close }: {
  store: Store; host: string; machine: string | undefined; folders: readonly string[]; close: () => void;
}) {
  const sessions = sessionsForSecrets(store.agents.value[host] ?? []);
  const first = folders[0];
  const chosen = useSignal(first ? `f:${first}` : sessions[0] ? `a:${sessions[0].id}` : "");
  const result = useSignal<EnvResolved | null>(null);
  const problem = useSignal<string | null>(null);
  useEffect(() => {
    const pick = chosen.value;
    if (!pick) return;
    result.value = null;
    problem.value = null;
    const params: EnvResolveRequest = pick.startsWith("f:") ? { folder: pick.slice(2) } : { agentID: pick.slice(2) as never };
    store.link.call("env/resolve", params, host).then((r) => { result.value = r; }, (error) => { problem.value = describe(error); });
  }, [chosen.value]);
  const title = result.value ? asSessionTitle(result.value.folder, machine) : w.asSession.replace("…", "");
  const repo = result.value ? repoName(result.value.folder) : "";
  return (
    <Modal label={title} close={close}>
      <div class="sheet-body env-resolve">
        <h2>{title}</h2>
        <select aria-label="Folder or session" value={chosen.value} onChange={(e) => { chosen.value = (e.currentTarget as HTMLSelectElement).value; }}>
          {folders.length > 0 && <optgroup label="Folders">{folders.map((f) => <option key={f} value={`f:${f}`} title={f}>{repoName(f)}</option>)}</optgroup>}
          {sessions.length > 0 && (
            <optgroup label="Running sessions">
              {sessions.map((a) => <option key={a.id} value={`a:${a.id}`}>{a.title ?? "Untitled session"}</option>)}
            </optgroup>
          )}
        </select>
        {problem.value && <p class="failure" role="alert">{problem.value}</p>}
        {result.value && (result.value.variables.length === 0 ? <p class="hint">{w.none}</p> : (
          <table class="env-resolved">
            <tbody>
              {result.value.variables.map((v) => (
                <tr key={v.name}>
                  <td><code>{v.name}</code></td>
                  <td><code>{v.value ?? "—"}</code></td>
                  <td class={v.value === undefined ? "warning" : "hint"}>{resolvedSource(v, repo)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
        <div class="sheet-actions">
          <button type="button" class="prominent" onClick={close}>Done</button>
        </div>
      </div>
    </Modal>
  );
}
