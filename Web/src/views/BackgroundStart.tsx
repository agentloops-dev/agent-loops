// New Session in Background (#5798), as the window's File ▸ New Session in Background (⇧⌘N): the start form in a
// dialog over the page. Start starts the session and closes the dialog; the page stays where it was, and the new
// session shows in the sidebar. Escape or Cancel closes it and keeps what was typed for the next time. The browser
// keeps ⇧⌘N for its private window, so this opens from ⌘K and the sidebar's + menu, with no key of its own.
import { signal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import type { StartRequest } from "../protocol/generated";
import type { Store } from "../model/store";
import { route } from "../route";
import { backgroundStartFolder, backgroundStartTitle, placeKey as keyOf, returnStarts, type StartPlace } from "../model/backgroundStart";
import { autoID, autoWords } from "../model/levels";
import { runtimeRuns } from "../model/runtimes";
import { keptLevels } from "../model/startForm";
import { orderedFolders } from "./Sidebar";
import { Modal } from "./Modal";

export { backgroundStartTitle };

/** The dialog's own form, apart from New Session's draft, so the two never change each other. */
const open = signal(false);
const prompt = signal("");
const place = signal<StartPlace | null>(null);
const runtimeID = signal(autoID);
const inWorktree = signal(false);
const canWorktree = signal(false);
const problem = signal<string | null>(null);
const starting = signal(false);

/** Opens the dialog; the page under it does not move. */
export function openBackgroundStart() {
  open.value = true;
}

export function BackgroundStartDialog({ store }: { store: Store }) {
  if (!open.value) return null;
  return <BackgroundStartForm store={store} />;
}

function BackgroundStartForm({ store }: { store: Store }) {
  const folders = orderedFolders(store.hosts.value, store.folders.value, store.macHostID.value);
  const several = store.hosts.value.length > 1;
  const chosen = place.value;
  const host = chosen?.host;
  const listed = host ? store.runtimes.value[host] ?? [] : [];
  const runs = runtimeRuns(listed);
  const runtimes = [...runs.available, ...runs.out];
  const startable = runtimes.length ? [autoID, ...runtimes.map((r) => r.runtime.id)] : [];

  // The folder at first, or again when the one kept has gone or nothing was typed.
  useEffect(() => {
    const kept = place.peek();
    if (!kept || !prompt.peek().trim() || !folders.some((f) => keyOf({ host: f.host.id, folder: f.folder.folder.folder }) === keyOf(kept))) {
      const r = route.peek();
      const live = folders.map((f) => ({ host: f.host.id, folder: f.folder.folder.folder }));
      place.value = backgroundStartFolder(live, { host: r.host, folder: r.folder });
    }
  }, []);
  // A new folder is a new question for the worktree.
  useEffect(() => {
    canWorktree.value = false;
    if (!chosen) return;
    let live = true;
    void store.worktrees(chosen.host, chosen.folder).then((answer) => {
      if (!live) return;
      canWorktree.value = answer?.canMakeNew === true;
      if (!canWorktree.value) inWorktree.value = false;
    });
    return () => { live = false; };
  }, [chosen && keyOf(chosen)]);
  useEffect(() => {
    if (startable.length && !startable.includes(runtimeID.value)) runtimeID.value = autoID;
  }, [startable.join(",")]);

  const close = () => { open.value = false; problem.value = null; };
  const ready = !!chosen && startable.length > 0 && !!prompt.value.trim() && !starting.value
    && store.hostIsOnline(chosen.host);
  const start = async () => {
    if (!ready || !chosen) return;
    starting.value = true;
    problem.value = null;
    const request: StartRequest = {
      runtimeID: runtimeID.value, cwd: chosen.folder as never, prompt: prompt.value.trim(), attachments: [],
      startOptions: { values: {}, extraArguments: [] }, additionalDirectories: [], mcpServers: [], labels: [],
      ...(inWorktree.value && canWorktree.value ? { worktree: { new: {} } } : {}),
      levels: keptLevels(),
      requestID: crypto.randomUUID() as never,
    };
    const id = await store.start(chosen.host, request);
    starting.value = false;
    if (id && typeof id === "object" && "autoRefusal" in id) { problem.value = id.autoRefusal; return; }
    if (id && typeof id === "object" && "message" in id) { problem.value = id.message ?? "The sandbox will not start."; return; }
    if (!id) { problem.value = "The session did not start. The prompt is kept."; return; }
    // Started, or kept for a stopped workspace: the dialog closes and the page stays; nothing is opened.
    prompt.value = "";
    inWorktree.value = false;
    close();
  };

  return (
    <Modal label={backgroundStartTitle} close={close}>
      <form method="dialog" class="sheet-body background-start" onSubmit={(e) => { e.preventDefault(); void start(); }}>
        <h2>{backgroundStartTitle}</h2>
        <div class="background-start-pickers">
          <label>Folder{" "}
            <select aria-label="Folder" value={chosen ? keyOf(chosen) : ""}
              onChange={(e) => {
                const k = (e.currentTarget as HTMLSelectElement).value;
                const f = folders.find((p) => keyOf({ host: p.host.id, folder: p.folder.folder.folder }) === k);
                if (f) place.value = { host: f.host.id, folder: f.folder.folder.folder };
              }}>
              {folders.map((f) => {
                const k = keyOf({ host: f.host.id, folder: f.folder.folder.folder });
                return <option key={k} value={k}>{several ? `${f.folder.name} · ${f.host.name}` : f.folder.name}</option>;
              })}
            </select>
          </label>
          <label>Runtime{" "}
            <select aria-label="Runtime" value={runtimeID.value} disabled={!startable.length}
              onChange={(e) => (runtimeID.value = (e.currentTarget as HTMLSelectElement).value)}>
              {startable.map((id) => (
                <option key={id} value={id}>{id === autoID ? autoWords : runtimes.find((r) => r.runtime.id === id)?.runtime.name ?? id}</option>
              ))}
            </select>
          </label>
        </div>
        <textarea aria-label="Prompt" placeholder="What should the agent do?" rows={5} autofocus value={prompt.value}
          onInput={(e) => (prompt.value = (e.currentTarget as HTMLTextAreaElement).value)}
          onKeyDown={(e) => {
            // Return starts, as the window's; Shift or Option and Return is a new line.
            if (returnStarts(e)) { e.preventDefault(); void start(); }
          }} />
        {problem.value && <p class="sheet-note failure" role="alert">{problem.value}</p>}
        {chosen && !store.hostIsOnline(chosen.host) && <p class="sheet-note failure">This folder's machine is offline.</p>}
        <div class="sheet-actions">
          {canWorktree.value && (
            <label class="background-start-worktree" title="Start the session in a new worktree of this folder">
              <input type="checkbox" checked={inWorktree.value}
                onChange={(e) => (inWorktree.value = (e.currentTarget as HTMLInputElement).checked)} /> Worktree
            </label>
          )}
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={!ready}>{starting.value ? "Starting…" : "Start"}</button>
        </div>
      </form>
    </Modal>
  );
}
