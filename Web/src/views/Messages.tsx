// Messages (#4344), as the window's MessagesView: the conversations of every messaging connector that
// follows the messaging contract, on every machine, from messages/conversations, in one list, the newest
// first, with Source and Unread filters; a conversation's page from messages/read, with a reply field
// (messages/reply, #4345) when its connector can send. Its text is outside content: drawn as Markdown
// by renderMarkdown, which draws no HTML, fetches no image and follows only http, https and mailto links.
import { signal, useSignal } from "@preact/signals";
import { useEffect, useRef } from "preact/hooks";
import type { MessagingMessage } from "../protocol/generated";
import type { Store } from "../model/store";
import { Capability } from "../protocol/capabilities.generated";
import { describe } from "../model/errors";
import { hostAnswers } from "../model/capabilities";
import { channelName } from "../model/channelLinks";
import { ago } from "../model/connectorSetup";
import {
  hasUnread, hostConversationsOf, inThread, lastConsequence, leaveThread, markAllHelp, markAllRead, markedRead, markingAllRead, markProblems, mergeConversations, mostPagesToFind,
  messagesStatusLine, unreadTotal, messagesProblems, messagesSources, nextConversation, nextHelp, nextRow, noConnector, noConnectorIn,
  noConversations, noMachine, noMessages, noUnread, outside, replyInThread, replyLabel, replyPlaceholder, replyText, send, senderName,
  sending, sentAs, setUp, sourceGlyph, unreadWords, type HostConversations, type MessagesRow,
} from "../model/messages";
import { go, routeHash } from "../route";
import { pageFooterState } from "../model/workFooter";
import { PlaceholderRows, RefreshButton, WorkFooter } from "./WorkParts";
import { renderMarkdown } from "../render/markdown";
import { Glyph } from "./Icon";

/** The list's state, for the tab: a conversation's page goes back to it as it was left. */
const lists = signal<HostConversations[] | null>(null);
const source = signal("");
const unreadOnly = signal(false);
const asked = signal(0);
/** Goes up each time a conversation or all of them were marked read (#5137), so the grey line reads its count again. */
const marked = signal(0);
const markingAll = signal(false);
const markLines = signal<string[]>([]);

const messagesHosts = (store: Store) => store.hosts.value.filter((h) => hostAnswers(h.state) && store.has(h.id, Capability.messages1));
const markHosts = (store: Store) => messagesHosts(store).filter((h) => store.has(h.id, Capability.messagesMarkRead1));

/** Mark all as read (#5137): every machine that can marks every conversation of its connectors read; then the list is
 * read again, as the services now say. */
async function markAll(store: Store) {
  if (markingAll.value) return;
  markingAll.value = true;
  markLines.value = [];
  const answers = await Promise.all(markHosts(store).map(async (h) => {
    const name = h.name || h.id;
    try {
      return { name, marked: await store.link.call("messages/markAllRead", {}, h.id) };
    } catch (e) {
      return { name, problem: describe(e) };
    }
  }));
  markLines.value = markProblems(answers);
  markingAll.value = false;
  marked.value++;
  asked.value++;
}

/** Where the next page of messages/conversationsAll starts (#5571); none at the end. */
const nextCursor = signal<string | undefined>(undefined);

interface ConversationsPage { lists: HostConversations[]; cursor: string | undefined }

/** Every machine's conversations in one call to the control plane (#5571): the first page, or the one after `cursor`, added to `into`. */
async function readLists(store: Store, cursor?: string, into: HostConversations[] = []): Promise<ConversationsPage> {
  try {
    const got = await store.link.call("messages/conversationsAll",
      { limit: 50, ...unreadOnly.value ? { unread: true } : {}, ...cursor ? { cursor } : {} });
    return { lists: hostConversationsOf(got, into), cursor: got.cursor };
  } catch (e) {
    return { lists: [...into, { host: "", name: "Control plane", conversations: [], errors: [], problem: describe(e) }], cursor: undefined };
  }
}

/** A read's answer drawn, while no newer read started. */
const take = (got: ConversationsPage) => { nextCursor.value = got.cursor; lists.value = got.lists; };

/** Reads the lists when the machines or the Unread filter change, or on Refresh. */
function useLists(store: Store) {
  // A new tab knows its machines from this browser's copy before its link is open and its lock is read (#5482): it reads
  // once the link is open, and again once it is in the lock, when it can ask the machines itself.
  const up = useSignal(store.link.state.kind === "open");
  useEffect(() => {
    const opened = () => { if (store.link.state.kind === "open") up.value = true; };
    opened();
    const stop = store.link.onState(opened);
    return () => { stop(); };
  }, [store]);
  const hostKey = messagesHosts(store).map((h) => h.id).join(",");
  useEffect(() => {
    let current = true;
    lists.value = null;
    if (!up.value) return;
    void readLists(store).then((got) => { if (current) take(got); });
    return () => { current = false; };
  }, [hostKey, asked.value, unreadOnly.value, up.value, store.machineLock.device.value.kind]);
}

/** How often the grey line is read again: the count asks every messaging connector over MCP. */
const statusEvery = 60_000;

/** The grey line at the head of Messages (#4934), as the window's sidebar row: the unread count, and what the newest
 * message.received woke ("12 unread · woke Fix login bug 4 min ago"), across every machine. Read on a timer; the count
 * is left out while it fails. No machine that reads messages: no line. */
function MessagesStatusLine({ store }: { store: Store }) {
  const unread = useSignal<number | undefined>(undefined);
  const now = useSignal(Date.now());
  const hosts = messagesHosts(store);
  const hostKey = hosts.map((h) => h.id).join(",");
  useEffect(() => {
    let current = true;
    const read = async () => {
      const ids = messagesHosts(store).map((h) => h.id);
      // One call for every machine (#5571); a failed one leaves the count out.
      const got = await store.link.call("messages/conversationsAll", { limit: 50, unread: true }).then((list) => hostConversationsOf(list), () => null);
      await store.loadEventsAll(ids);
      if (!current) return;
      unread.value = got?.length ? unreadTotal(got) ?? 0 : got ? 0 : undefined;
      now.value = Date.now();
    };
    void read();
    const timer = setInterval(() => { void read(); }, statusEvery);
    return () => { current = false; clearInterval(timer); };
  }, [hostKey, marked.value]);
  if (hosts.length === 0) return null;
  const events = hosts.flatMap((h) => store.events.value[h.id]?.events ?? []);
  const line = messagesStatusLine(unread.value, lastConsequence(events), now.value);
  return line ? <p class="hint quiet small messages-status" role="status">{line}</p> : null;
}

export function MessagesList({ store }: { store: Store }) {
  useLists(store);
  const more = useSignal(false);
  const hosts = messagesHosts(store);
  const l = lists.value;
  const rows = l ? mergeConversations(l, source.value) : [];
  const firstFolder = Object.entries(store.folders.value).flatMap(([host, fs]) => fs.map((f) => ({ host, folder: f.folder.folder })))[0];
  const loadMore = async () => {
    if (!l || more.value) return;
    more.value = true;
    const got = await readLists(store, nextCursor.value, l);
    if (lists.value === l) take(got);
    more.value = false;
  };

  return (
    <div class="work-list messages-list">
      <MessagesStatusLine store={store} />
      <div class="work-filters" role="group" aria-label="Filters">
        <label>Source <select value={source.value} onChange={(e) => { source.value = (e.target as HTMLSelectElement).value; }}>
          <option value="">All</option>
          {messagesSources(l ?? []).map((s) => <option key={s} value={s}>{channelName(s)}</option>)}
        </select></label>
        <label><input type="checkbox" checked={unreadOnly.value}
          onChange={(e) => { unreadOnly.value = (e.target as HTMLInputElement).checked; }} /> Unread</label>
        {markHosts(store).length > 0 && (
          <button type="button" class="link" title={markAllHelp} disabled={markingAll.value || !hasUnread(l ?? [])}
            onClick={() => { void markAll(store); }}>{markingAll.value ? markingAllRead : markAllRead}</button>
        )}
        <button type="button" class="link" onClick={() => { asked.value++; }}>Refresh</button>
      </div>
      {hosts.length === 0 ? <p class="hint">{noMachine}</p>
        : l === null ? <PlaceholderRows />
        : noConnectorIn(l) && !unreadOnly.value ? (
          <div class="messages-empty">
            <p class="hint">{noConnector}</p>
            {firstFolder && <button type="button" class="link" title="The folder's Connectors, with its Messaging slot"
              onClick={() => go({ host: firstFolder.host, folder: firstFolder.folder, mcp: true })}>{setUp}</button>}
          </div>
        ) : <>
          {[...messagesProblems(l), ...markLines.value].map((p) => <p key={p} class="hint">{p}</p>)}
          {rows.length === 0 ? <p class="hint">{unreadOnly.value ? noUnread : noConversations}</p> : (
            <ul class="work-rows">
              {rows.map((r) => <ConversationRow key={r.key} row={r} />)}
            </ul>
          )}
          {nextCursor.value && (
            <button type="button" class="link" disabled={more.value} onClick={() => { void loadMore(); }}>{more.value ? "Loading…" : "More"}</button>
          )}
        </>}
    </div>
  );
}

function ConversationRow({ row }: { row: MessagesRow }) {
  const c = row.item.conversation;
  const unread = unreadWords(c);
  return (
    <li data-key={row.key}>
      <a class={`work-row messages-row${unread ? " unread" : ""}`} href={routeHash({ nav: "work", section: "messages", item: row.key })}>
        <span class="work-row-head">
          <span class="messages-source" title={channelName(c.source)} aria-label={channelName(c.source)}><Glyph of={sourceGlyph(c.source)} /></span>
          {c.sender && <span class="messages-sender" title={c.sender.identity}>{senderName(c.sender)}</span>}
          <span class={`work-title messages-title${c.sender ? " quiet" : ""}`}>{c.title}</span>
          {unread && <span class="messages-unread small">{unread}</span>}
          {c.lastActivity && <span class="quiet small docs-updated">{ago(new Date(c.lastActivity))}</span>}
        </span>
      </a>
    </li>
  );
}

/** A conversation's page: its messages, the newest first, each with its sender, time, text and a link to the
 * original; older pages with Older; under them the reply field, when the connector can send. Found by its key
 * in the list, read again when the tab was opened on it. The footer says how old the messages are, and Refresh reads them
 * again (#7076). */
export function ConversationPage({ store, conversationKey }: { store: Store; conversationKey: string }) {
  useLists(store);
  const messages = useSignal<MessagingMessage[] | null>(null);
  const cursor = useSignal<string | undefined>(undefined);
  const problem = useSignal<string | null>(null);
  const loading = useSignal(false);
  /** When the newest page was read. */
  const readAt = useSignal<number | undefined>(undefined);
  /** Goes up with each read of the newest page: an older read that ends after it is dropped. */
  const generation = useRef(0);
  const draft = useSignal("");
  const thread = useSignal<string | undefined>(undefined);
  const busy = useSignal(false);
  const sendProblem = useSignal<string | null>(null);
  const l = lists.value;
  const row = l ? mergeConversations(l).find((r) => r.key === conversationKey) : undefined;
  // A new tab on a conversation past the first page (#5482) reads further pages, a few at most, until it has it.
  const pages = useSignal(1);
  const finding = useSignal(false);
  const looking = l !== null && !row && nextCursor.value !== undefined && pages.value < mostPagesToFind;
  useEffect(() => {
    if (!looking) return;
    pages.value++;
    finding.value = true;
    void readLists(store, nextCursor.value, l).then((got) => {
      if (lists.value === l) take(got);
      finding.value = false;
    });
  }, [l, looking]);
  // Next (#5137): the next conversation in the list as it is filtered and sorted now.
  const next = l ? nextRow(mergeConversations(l, source.value), conversationKey) : undefined;
  const goNext = () => { if (next) go({ nav: "work", section: "messages", item: next.key }); };
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "ArrowDown" && e.altKey && (e.metaKey || e.ctrlKey) && !e.shiftKey) { e.preventDefault(); goNext(); }
    };
    window.addEventListener("keydown", onKey);
    return () => { window.removeEventListener("keydown", onKey); };
  }, [next?.key]);
  const replyAs = row?.item.replyAs;
  const reply = async () => {
    const text = replyText(draft.value);
    if (!row || !text || busy.value) return;
    busy.value = true;
    sendProblem.value = null;
    try {
      const got = await store.link.call("messages/reply", {
        server: row.item.server, folder: row.item.folder, conversation: row.item.conversation.id, text,
        ...thread.value ? { thread: thread.value } : {},
      }, row.host);
      messages.value = [got.message, ...messages.value ?? []];
      draft.value = "";
    } catch (e) {
      sendProblem.value = describe(e);
    }
    busy.value = false;
  };
  const read = async (older: boolean) => {
    if (!older) generation.current++;
    const mine = generation.current;
    if (!row) return;
    loading.value = true;
    try {
      const got = await store.link.call("messages/read", {
        server: row.item.server, folder: row.item.folder, conversation: row.item.conversation.id, limit: 50,
        ...older && cursor.value ? { cursor: cursor.value } : {},
      }, row.host);
      if (mine !== generation.current) return;
      messages.value = [...older ? messages.value ?? [] : [], ...got.messages];
      cursor.value = got.cursor;
      problem.value = null;
      if (!older) readAt.value = Date.now();
      if (got.markedRead && lists.value) {
        lists.value = markedRead(lists.value, row.key);
        marked.value++;
      }
    } catch (e) {
      if (mine === generation.current) problem.value = describe(e);
    }
    if (mine === generation.current) loading.value = false;
  };
  const refresh = () => { void read(false); };
  useEffect(() => { messages.value = null; problem.value = null; readAt.value = undefined; loading.value = false; void read(false); }, [row?.key, row?.host]);

  if (l === null || looking || finding.value) return <PlaceholderRows count={4} />;
  const problems = row ? [] : messagesProblems(l);
  if (problems.length > 0) return (
    <>
      {problems.map((p) => <p key={p} class="hint">{p}</p>)}
      <button type="button" class="link" onClick={() => { asked.value++; }}>Refresh</button>
    </>
  );
  if (!row) return <p class="hint">This conversation is not in the list any more. <button type="button" class="link" onClick={() => go({ nav: "work", section: "messages" })}>Messages</button></p>;
  const c = row.item.conversation;
  return (
    <article class="work-page messages-page work-footed">
      <div class="work-page-bar">
        <button type="button" title="Back" aria-label="Back" onClick={() => history.back()}>‹</button>
        <button type="button" title="Forward" aria-label="Forward" onClick={() => history.forward()}>›</button>
        <button type="button" class="link" onClick={() => go({ nav: "work", section: "messages" })}>All messages</button>
        <button type="button" class="link" title={nextHelp} disabled={!next} onClick={goNext}>{nextConversation} ↓</button>
        <RefreshButton busy={loading.value} onClick={refresh} />
      </div>
      <header class="work-page-head">
        <h2><span class="work-title">{c.title}</span></h2>
        <p class="quiet small">
          <Glyph of={sourceGlyph(c.source)} /> {channelName(c.source)} ▸ {row.item.server}
          {c.link && <> · <a href={c.link} target="_blank" rel="noopener noreferrer">Open in {channelName(c.source)}</a></>}
        </p>
      </header>
      <p class="hint">{outside}</p>
      {messages.value === null && problem.value ? <p class="hint">{problem.value}</p>
        : messages.value === null ? <PlaceholderRows count={4} />
        : messages.value.length === 0 ? <p class="hint">{noMessages}</p>
        : (
          <ul class="messages-rows">
            {messages.value.map((m) => (
              <li key={m.id} class="messages-message">
                <div class="messages-head">
                  <strong title={m.sender.identity}>{senderName(m.sender)}</strong>
                  <span class="quiet small">{new Date(m.time).toLocaleString(undefined, { month: "short", day: "numeric", hour: "numeric", minute: "2-digit" })}</span>
                  {replyAs && m.thread && <button type="button" class="link small messages-thread"
                    onClick={() => { thread.value = m.thread; }}>{replyInThread}</button>}
                  {m.link && <a class="small messages-original" href={m.link} target="_blank" rel="noopener noreferrer">Original</a>}
                </div>
                <div class="messages-text markdown">{renderMarkdown(m.text)}</div>
              </li>
            ))}
          </ul>
        )}
      {cursor.value && messages.value !== null && (
        <button type="button" class="link" disabled={loading.value} onClick={() => { void read(true); }}>{loading.value ? "Loading…" : "Older"}</button>
      )}
      {replyAs && messages.value !== null && (
        <form class="messages-reply" onSubmit={(e) => { e.preventDefault(); void reply(); }}>
          {thread.value && <p class="quiet small">↩ {inThread} · <button type="button" class="link" onClick={() => { thread.value = undefined; }}>{leaveThread}</button></p>}
          <div class="messages-reply-row">
            <textarea rows={2} aria-label={replyLabel} placeholder={replyPlaceholder} value={draft.value} disabled={busy.value}
              onInput={(e) => { draft.value = (e.target as HTMLTextAreaElement).value; }}
              onKeyDown={(e) => { if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) { e.preventDefault(); void reply(); } }} />
            <button type="submit" title="Send the reply (⌘↩)" disabled={busy.value || !replyText(draft.value)}>{busy.value ? sending : send}</button>
          </div>
          <p class="quiet small">{sentAs(replyAs)}</p>
          {sendProblem.value && <p class="hint">{sendProblem.value}</p>}
        </form>
      )}
      <WorkFooter state={pageFooterState(messages.value !== null, loading.value, false, problem.value ? [problem.value] : [])}
        at={readAt.value} retry={refresh} />
    </article>
  );
}
