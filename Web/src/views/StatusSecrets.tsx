// The sidebar's Status ▸ Secrets and Status ▸ Accounts rows (#4161, #4931), as the window's StatusSecretsRow
// (Shared/UI/UserScope/StatusSecretsPage.swift): the newest operation's age, and the line under it. Each row opens its
// page (#5257, #5258).
import { useEffect } from "preact/hooks";
import type { Store } from "../model/store";
import { accountLines, newestAt, recentOperation, secretLines, shortAgo, statusSecretsWords as w, type StatusLine } from "../model/statusSecrets";

type List = "secrets" | "accounts";

/** A Status list's lines, from what the store last read. */
export function statusLines(store: Store, list: List): StatusLine[] {
  const status = store.userScopeStatus.value;
  if (!status) return [];
  return list === "secrets" ? secretLines(status.secrets) : accountLines(status.accounts, status.secrets);
}

/** Reads the lists when the Status section opens and on each control/userSettingsChanged. */
export function useUserScopeStatus(store: Store, on: boolean) {
  const changes = store.userScopeChanges.value;
  useEffect(() => { if (on) void store.loadUserScopeStatus(); }, [changes, on]);
}

/** The sidebar row's end: the newest operation's age. No warning tint: "not backed up" is on the page. */
export function StatusListMark({ store, list }: { store: Store; list: List }) {
  const at = newestAt(statusLines(store, list));
  return at !== null ? <span class="quiet small" title={new Date(at).toLocaleString()}>{shortAgo(at)}</span> : null;
}

/** Under the sidebar row, in grey, as the window's (#4931): the secret or account the newest operation was on, the
 * operation when it is not a read, and the session that did it, when it is less than 4 hours old. */
export function StatusListLastLine({ store, list }: { store: Store; list: List }) {
  const title = (id: string) => Object.values(store.agents.value).flat().find((a) => a.id === id)?.title;
  const last = recentOperation(statusLines(store, list), Date.now(), title);
  return last ? <p class="page-need quiet small" aria-label={`${w.lastLabel} ${last}`}>{last}</p> : null;
}
