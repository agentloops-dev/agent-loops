// New Session in Background (#5798), as the window's File ▸ New Session in Background (⇧⌘N): New Session's own form
// (`NewAgent`, with its Project and On, pills and prompt) in a dialog over the page. A start closes the dialog and the
// page stays where it was; the new session shows in the sidebar. Escape or the dialog's close closes it, and the
// prompt stays, as New Session's draft. The browser keeps ⇧⌘N for its private window, so this opens from ⌘K and the
// sidebar's + menu, with no key of its own.
import { signal } from "@preact/signals";
import type { Store } from "../model/store";
import { route } from "../route";
import { backgroundStartFolder, backgroundStartTitle, placeKey, type StartPlace } from "../model/backgroundStart";
import { orderedFolders } from "./Sidebar";
import { Modal } from "./Modal";
import { NewAgent } from "./NewAgent";

export { backgroundStartTitle };

const open = signal(false);
/** The dialog's folder, kept between openings; apart from the page's. */
const place = signal<StartPlace | null>(null);

/** Opens the dialog, in the folder open (or the open session's) unless one is kept; the page under it does not move. */
export function openBackgroundStart() {
  open.value = true;
}

export function BackgroundStartDialog({ store }: { store: Store }) {
  if (!open.value) return null;
  const folders = orderedFolders(store.hosts.value, store.folders.value, store.macHostID.value);
  const live = folders.map((f) => ({ host: f.host.id, folder: f.folder.folder.folder }));
  const kept = place.value && live.find((p) => placeKey(p) === placeKey(place.value!));
  const r = route.peek();
  const here = kept ?? backgroundStartFolder(live, { host: r.host, folder: r.folder });
  const close = () => { open.value = false; };
  const summary = here && folders.find((f) => placeKey({ host: f.host.id, folder: f.folder.folder.folder }) === placeKey(here));
  return (
    <Modal label={backgroundStartTitle} close={close}>
      <div class="background-start">
        {here && summary ? (
          <NewAgent key={placeKey(here)} store={store} host={here.host} folder={here.folder} folderName={summary.folder.name}
            down={!store.hostIsOnline(here.host)}
            inBackground={{ started: close, move: (host, folder) => { place.value = { host, folder }; } }} />
        ) : <p class="quiet">Add a folder to start a session.</p>}
      </div>
    </Modal>
  );
}
