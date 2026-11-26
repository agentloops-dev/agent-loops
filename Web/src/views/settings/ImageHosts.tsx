// Settings ▸ Machines ▸ Image Hosts (#7140, port of #7139, epic #7136): the hosts whose images a
// machine's Work pages load, with Remove. The list belongs to the machine whose daemon reads the
// images, so the section always draws its Machine menu, also when there is a Mac: a page can be read
// by another machine. The kept list first, then the machine's. A host is added only from a blocked
// image. Listed in ./sections.ts; the Mac's is App/Sources/Settings/ImageHostsSettingsView.swift.
import { signal } from "@preact/signals";
import { useEffect } from "preact/hooks";
import type { Store } from "../../model/store";
import { Capability } from "../../protocol/capabilities.generated";
import type { ImageHosts } from "../../protocol/generated";
import { fromWireDate } from "../../protocol/dates";
import {
  allowedOn, couldNotRead, couldNotRemove, footerWords, ImageHostsList, imageHostsWords, offlineWords, removeLabel,
} from "../../model/imageHosts";
import { keepWork, keptWorkRead } from "../../model/workCache";
import { MachinePicker } from "../MachinePicker";
import { LastUpdate } from "../LastUpdate";
import { MessageBanner } from "../MessageKinds";

const chosen = signal<string | null>(null);
const lists = new Map<string, ImageHostsList>();

function listFor(store: Store, machine: string): ImageHostsList {
  let list = lists.get(machine);
  if (!list) {
    list = new ImageHostsList({
      kept: () => keptWorkRead<ImageHosts>("imageHosts", machine),
      list: () => store.link.call("imageHosts/list", {}, machine),
      remove: (host) => store.link.call("imageHosts/remove", { host }, machine),
      keep: (got) => keepWork("imageHosts", machine, got),
    });
    lists.set(machine, list);
  }
  return list;
}

/** Shown when a machine keeps the list. */
export function imageHostsSectionShows(store: Store): boolean {
  return store.hosts.value.some((h) => store.has(h.id, Capability.imageHosts1));
}

export function ImageHostsSection({ store, down }: { store: Store; down: boolean }) {
  const mac = store.macHostID.value;
  // The machines that keep the list, also one that is offline: the Mac first, then by name.
  const machines = store.hosts.value.filter((h) => store.has(h.id, Capability.imageHosts1))
    .sort((a, b) => Number(b.id === mac) - Number(a.id === mac) || a.name.localeCompare(b.name));
  const machine = machines.find((h) => h.id === chosen.value) ?? machines.find((h) => h.id === mac) ?? machines[0];
  const online = machine !== undefined && !down && store.hostIsOnline(machine.id);
  const list = machine ? listFor(store, machine.id) : undefined;
  useEffect(() => { if (machine && list) void list.load(online); }, [machine?.id, online]);
  if (!machine || !list) {
    return (
      <section aria-labelledby="settings-image-hosts">
        <h3 class="section-head" id="settings-image-hosts">{imageHostsWords.title}</h3>
        <p class="hint">{imageHostsWords.noMachine}</p>
      </section>
    );
  }
  const name = machine.name;
  const hosts = list.hosts.value;
  const failed = list.notRemoved.value;
  const problem = list.problem.value;
  const readAt = list.readAt.value;
  return (
    <section aria-labelledby="settings-image-hosts" class="image-hosts">
      {failed ? (
        <MessageBanner tone="failure" actions={
          <button type="button" disabled={!online} onClick={() => void list.remove(failed.host)}>{imageHostsWords.tryAgain}</button>}>
          <span title={failed.why}>{couldNotRemove(failed.host, name)}</span>
        </MessageBanner>
      ) : problem ? (
        <MessageBanner tone="failure" actions={
          <button type="button" disabled={!online} onClick={() => void list.load(true)}>{imageHostsWords.tryAgain}</button>}>
          <span class="strong">{couldNotRead(name)}</span><span>{problem}</span>
        </MessageBanner>
      ) : null}
      <div class="image-hosts-head">
        <h3 class="section-head" id="settings-image-hosts">{imageHostsWords.title}</h3>
        <span class="image-hosts-fresh">
          {list.reading.value && <span class="spinner" aria-hidden="true" />}
          {readAt !== undefined && <LastUpdate at={readAt} />}
        </span>
      </div>
      <label class="setting">Machine{" "}
        <MachinePicker store={store} label="Machine" value={machine.id} disabled={machines.length < 2}
          skip={(h) => !machines.some((m) => m.id === h.id)} onPick={(id) => { if (id) chosen.value = id; }} />
      </label>
      <div class="image-hosts-list">
        {hosts === null ? (
          online && !problem ? <p class="image-hosts-empty"><span class="spinner" aria-hidden="true" /> {imageHostsWords.firstRead}</p> : null
        ) : hosts.length === 0 ? (
          <p class="image-hosts-empty"><b>{imageHostsWords.none}</b><br />{imageHostsWords.noneDetail}</p>
        ) : hosts.map((h) => (
          <div class="setting image-hosts-row" key={h.host}>
            <span class="image-hosts-host">
              {h.host}
              <span class="quiet small">{allowedOn(fromWireDate(h.addedAt).getTime())}</span>
              {failed?.host === h.host && <span class="failure small">{imageHostsWords.notRemoved}</span>}
            </span>
            <button type="button" class="image-hosts-remove" aria-label={removeLabel(h.host)} disabled={!online}
              onClick={() => void list.remove(h.host)}>{imageHostsWords.remove}</button>
          </div>
        ))}
      </div>
      {!online && <p class="hint">{offlineWords(name)}</p>}
      <p class="hint">{footerWords(name)}</p>
    </section>
  );
}
