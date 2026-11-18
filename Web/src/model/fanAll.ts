// Each machine's answer, asked by this page itself, when it is in its lock (#5996).
//
// The control plane's one call for every machine (#5571, #5592) asks each machine in its own name, with no
// signature, and a machine that holds a lock refuses that: it takes a read only on a channel a device of the lock
// opened (specs/3399-machine-lock, D6). So in a lock the page asks each machine itself, over its own checked channel,
// and makes the same answer the control plane would: the same machines, bounds and order, so no caller changes.

import type {
  ConnectionsList, DocsList, DocsListAll, DocsListAllRequest, EventsListAll, EventsListAllRequest, EventsPage, FeatureUsage,
  FeatureUsageAll, ForgeBoards, ForgeBoardsAll, ForgeList, ForgeListAll, ForgeListAllRequest, LineageForecast, LineageForecastAll,
  LineageForecastRequest, MachineRef, MessagesConversation, MessagesConversations, MessagesConversationsAll,
  MessagesConversationsAllRequest, ProjectMachine, ConnectionsListAll, Doc,
} from "../protocol/generated";
import { Capability } from "../protocol/capabilities.generated";

/** The most machines one call asks, as the control plane's mostFanMachines. */
export const mostFanMachines = 50;
/** How long a machine has to answer, as the control plane's fanPatience. */
export const fanPatience = 5_000;

/** A machine this page may ask. */
export interface FanHost { id: string; name: string; online: boolean }

/** What the answers are made with: the page's machines that have a capability, and a call to one of them. */
export interface FanHere {
  hosts(capability: string): FanHost[];
  ask(method: string, params: unknown, host: string): Promise<unknown>;
}

const limitOf = (said: number | undefined, byDefault: number, most: number) =>
  said === undefined || said < 1 ? byDefault : Math.min(Math.floor(said), most);

/** One machine's answer within fanPatience; null when it did not answer, or answered with an error. */
async function askOne<T>(here: FanHere, host: string, method: string, params: unknown): Promise<T | null> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const late = new Promise<null>((resolve) => { timer = setTimeout(() => resolve(null), fanPatience); });
  try {
    return await Promise.race([here.ask(method, params, host).then((r) => r as T, () => null), late]);
  } finally {
    clearTimeout(timer);
  }
}

/** The machines to ask, at most mostFanMachines, and how many were past it. */
function fanHosts(here: FanHere, capability: string): { hosts: FanHost[]; notAsked: number } {
  const all = here.hosts(capability);
  return { hosts: all.slice(0, mostFanMachines), notAsked: Math.max(0, all.length - mostFanMachines) };
}

const machine = (h: FanHost): ProjectMachine => ({ machine: h.id as MachineRef, machineName: h.name });

/** Each host's answer at once, in the order of hosts, and those that did not answer. An offline one is not asked. */
async function askEach<T>(here: FanHere, hosts: FanHost[], method: string, params: (h: FanHost) => unknown) {
  const got = await Promise.all(hosts.map((h) => h.online ? askOne<T>(here, h.id, method, params(h)) : Promise.resolve(null)));
  return {
    items: hosts.flatMap((h, i) => got[i] === null ? [] : [{ host: h, item: got[i] as T }]),
    unreached: hosts.filter((_, i) => got[i] === null).map(machine),
  };
}

/** Where one machine is in a cursor, as the control plane's fanPlace: its page's cursor and how many of it were taken. */
interface FanPlace { p?: string; n?: number; d?: boolean }

const encodeCursor = (places: Record<string, FanPlace>) => btoa(JSON.stringify(places));

function decodeCursor(cursor: string | undefined): Record<string, FanPlace> {
  if (!cursor) return {};
  try {
    const places = JSON.parse(atob(cursor)) as unknown;
    if (places && typeof places === "object" && !Array.isArray(places)) return places as Record<string, FanPlace>;
  } catch { /* below */ }
  throw new Error("That is not a cursor this answer gave.");
}

/** The control plane's fanOut: each host's page from its place, merged newest first, `limit` of them, and the cursor. */
async function fanOut<T>(hosts: FanHost[], cursor: string | undefined, limit: number,
  ask: (host: FanHost, page: string | undefined) => Promise<{ items: T[]; next?: string } | null>, newer: (a: T, b: T) => boolean) {
  const places = decodeCursor(cursor);
  const pages = await Promise.all(hosts.map((h) => places[h.id]?.d || !h.online ? Promise.resolve(null) : ask(h, places[h.id]?.p)));
  const items: { host: FanHost; item: T }[] = [];
  const unreached: ProjectMachine[] = [];
  const next = hosts.map((h, i) => Math.min(places[h.id]?.n ?? 0, pages[i]?.items.length ?? 0));
  hosts.forEach((h, i) => { if (!places[h.id]?.d && pages[i] === null) unreached.push(machine(h)); });
  while (items.length < limit) {
    let best = -1;
    for (let i = 0; i < hosts.length; i++) {
      const page = pages[i];
      if (!page || next[i]! >= page.items.length) continue;
      if (best < 0 || newer(page.items[next[i]!]!, pages[best]!.items[next[best]!]!)) best = i;
    }
    if (best < 0) break;
    items.push({ host: hosts[best]!, item: pages[best]!.items[next[best]!]! });
    next[best]!++;
  }
  let more = false;
  hosts.forEach((h, i) => {
    let place = places[h.id] ?? {};
    if (place.d) { places[h.id] = { d: true }; return; }
    const page = pages[i];
    if (!page) place = { d: true };
    else if (next[i]! < page.items.length) place = { ...place, n: next[i]! };
    else if (page.next) place = { p: page.next };
    else place = { d: true };
    places[h.id] = place;
    more ||= !place.d;
  });
  return { items, unreached, cursor: more ? encodeCursor(places) : undefined };
}

/**
 * The answer to one of the link's fanAllMethods, made here; undefined for another method. It throws only for a bad request, as
 * the control plane would.
 */
export async function answerHere(method: string, params: unknown, here: FanHere): Promise<unknown> {
  switch (method) {
    case "forge/listAll": {
      const request = params as ForgeListAllRequest;
      if (request.kind !== "issues" && request.kind !== "pulls") throw new Error("kind is issues or pulls.");
      // Only a machine that knows boards is asked for a board's list, as one that does not would list it all (#3282).
      let { hosts, notAsked } = fanHosts(here, request.board ? Capability.forgeBoards1 : Capability.forge1);
      const cursors = new Map((request.pages ?? []).map((p) => [p.machine as string, p.cursor]));
      if (request.pages) {
        hosts = hosts.filter((h) => cursors.has(h.id));
        notAsked = 0;
      }
      const limit = limitOf(request.limit, 50, 100);
      const got = await askEach<ForgeList>(here, hosts, `forge/${request.kind}`, (h) => ({
        ...request.state !== undefined && { state: request.state }, ...request.board !== undefined && { board: request.board }, limit,
        ...cursors.get(h.id) && { cursor: cursors.get(h.id) },
      }));
      return {
        lists: got.items.map(({ host, item }) => ({ ...machine(host), list: item })), unreached: got.unreached, notAsked,
      } satisfies ForgeListAll;
    }
    case "forge/boardsAll": {
      const { hosts, notAsked } = fanHosts(here, Capability.forgeBoards1);
      const got = await askEach<ForgeBoards>(here, hosts, "forge/boards", () => ({}));
      return { boards: got.items.map(({ host, item }) => ({ ...machine(host), boards: item })), unreached: got.unreached, notAsked } satisfies ForgeBoardsAll;
    }
    case "lineage/forecastAll": {
      const { hosts, notAsked } = fanHosts(here, Capability.lineageForecast1);
      const got = await askEach<LineageForecast>(here, hosts, "lineage/forecast", () => params as LineageForecastRequest);
      return { forecasts: got.items.map(({ host, item }) => ({ ...machine(host), forecast: item })), unreached: got.unreached, notAsked } satisfies LineageForecastAll;
    }
    case "reports/featuresAll": {
      const { hosts, notAsked } = fanHosts(here, Capability.features1);
      const got = await askEach<FeatureUsage>(here, hosts, "reports/features", () => ({}));
      return { usage: got.items.map(({ host, item }) => ({ ...machine(host), usage: item })), unreached: got.unreached, notAsked } satisfies FeatureUsageAll;
    }
    case "events/listAll": {
      const { hosts, notAsked } = fanHosts(here, Capability.events1);
      const limit = limitOf((params as EventsListAllRequest).limit, 50, 100);
      const got = await askEach<EventsPage>(here, hosts, "events/list", () => ({ limit }));
      return { pages: got.items.map(({ host, item }) => ({ ...machine(host), page: item })), unreached: got.unreached, notAsked } satisfies EventsListAll;
    }
    case "connections/listAll": {
      const { hosts, notAsked } = fanHosts(here, Capability.connections1);
      const got = await askEach<ConnectionsList>(here, hosts, "connections/list", () => ({}));
      return { lists: got.items.map(({ host, item }) => ({ ...machine(host), connections: item })), unreached: got.unreached, notAsked } satisfies ConnectionsListAll;
    }
    case "docs/listAll": {
      const request = params as DocsListAllRequest;
      const limit = limitOf(request.limit, 200, 500);
      const { hosts, notAsked } = fanHosts(here, Capability.docs1);
      const sources = new Map<string, DocsList["sources"]>();
      const got = await fanOut<Doc>(hosts, request.cursor, limit, async (h, page) => {
        // Each machine is asked for as many as the page has, as the newest may all be its.
        const list = await askOne<DocsList>(here, h.id, "docs/list", { limit, ...request.source !== undefined && { source: request.source }, ...page && { cursor: page } });
        if (!list) return null;
        sources.set(h.id, list.sources);
        return { items: list.docs, ...list.cursor && { next: list.cursor } };
      }, (a, b) => (a.updated ?? 0) > (b.updated ?? 0));
      return {
        docs: got.items.map(({ host, item }) => ({ ...machine(host), doc: item })),
        sources: hosts.flatMap((h) => (sources.get(h.id) ?? []).map((source) => ({ ...machine(h), source }))),
        unreached: got.unreached, notAsked, ...got.cursor && { cursor: got.cursor },
      } satisfies DocsListAll;
    }
    case "messages/conversationsAll": {
      const request = params as MessagesConversationsAllRequest;
      const limit = limitOf(request.limit, 20, 100);
      const { hosts, notAsked } = fanHosts(here, Capability.messages1);
      const errors = new Map<string, MessagesConversations["errors"]>();
      const got = await fanOut<MessagesConversation>(hosts, request.cursor, limit, async (h, page) => {
        const list = await askOne<MessagesConversations>(here, h.id, "messages/conversations",
          { limit, ...request.unread !== undefined && { unread: request.unread }, ...page && { cursor: page } });
        if (!list) return null;
        errors.set(h.id, list.errors);
        return { items: list.conversations, ...list.cursor && { next: list.cursor } };
      }, (a, b) => (a.conversation.lastActivity ?? "") > (b.conversation.lastActivity ?? ""));
      return {
        conversations: got.items.map(({ host, item }) => ({ ...machine(host), conversation: item })),
        errors: hosts.flatMap((h) => (errors.get(h.id) ?? []).map((error) => ({ ...machine(h), error }))),
        unreached: got.unreached, notAsked, ...got.cursor && { cursor: got.cursor },
      } satisfies MessagesConversationsAll;
    }
  }
  return undefined;
}
