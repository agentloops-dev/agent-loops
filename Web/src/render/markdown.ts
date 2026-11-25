// What an agent wrote, drawn as Markdown and never as HTML (071 FR-031, research R9).
//
// markdown-it parses with `html: false`, so raw HTML in a message arrives as text. Its tokens are
// turned into Preact nodes here, never into an HTML string, so nothing an agent says can reach an
// HTML sink. An image in a chat, or any image that is not a file beside a Markdown page, becomes
// a placeholder naming it: the page loads nothing from anywhere but itself (the CSP says so too).
// A picture beside a page is drawn from bytes `files/read` returns, and an image on a Work page
// from bytes `forge/image` returns (#4011). A link opens in a new tab,
// and only http, https and mailto links are links at all. A file: link, or a bare absolute path, is a link only
// where the page says how a file opens (a chat opens it in Files, #548), and then it never navigates.
// On a page, a relative link (`other.md`, `../docs/a.md`) is read against the page's own file and
// opens that file where the page says (#5068), as on the Mac and the Remote (#5067).
// An agents://agent/<id> link selects that session (#3353), as on the Mac and the Remote.
import MarkdownIt, { type Token } from "markdown-it";
import { pageImagePath } from "../model/pageImage";
import { pageLinkTarget } from "../model/pageLink";
import { agentLinkID, openAgentLink } from "../model/agentLink";
import { LocalPicture } from "./LocalPicture";
import { ForgePicture } from "./ForgePicture";
import { forgeImageURL } from "../model/forgeMarkdown";
import { memo } from "./memo";
import { highlightCode } from "./codeHighlight";
import { MermaidFence } from "./MermaidView";
import { isFence } from "../model/mermaid";
import { Fragment, h, type ComponentChildren } from "preact";

/** A Markdown page's pictures: read from beside the document, never from an address. */
export interface PageImages {
  document: string;
  /** Bumped when the folder changes, so a picture is read again. */
  revision: number;
  read: (path: string) => Promise<{ bytes: Uint8Array; type: string } | null>;
}

/**
 * A forge page's images (#4011): read through the machine that read the page, with forge/image,
 * never from their address. Only the Work page gives one; null is an image it does not read.
 */
export type ForgeImages = (url: string) => Promise<{ bytes: Uint8Array; type: string } | null>;

const parser = new MarkdownIt({ html: false, linkify: true, typographer: false, breaks: false });
// markdown-it drops a file: link to its bare text; it is kept as a link token so `build` can
// decide, and `build` follows it only through `openFile`, never as an address.
const validateLink = parser.validateLink.bind(parser);
parser.validateLink = (url) => validateLink(url) || fileLinkPath(url) !== null;

/** Opens a file on the agent's host by its full path: the chat's Files pane. */
export type OpenFile = (location: { path: string }) => void;

/**
 * A page's links to files (#5068): `document` is the page's own file, which a relative link is read
 * against, and `open` shows the file a link names. A file `takes` refuses stays words.
 */
export interface PageLinks {
  document: string;
  open: (path: string) => void;
  takes?: ((path: string) => boolean) | undefined;
}

/** The file a link on a page names, by an absolute path or one relative to the page, or null. */
function pageFile(href: string, links: PageLinks): string | null {
  const path = fileLinkPath(href);
  if (path) return path;
  const target = pageLinkTarget(href, links.document);
  return target?.kind === "file" ? target.path : null;
}

/** A button on a passage that is clicked or typed on does not also start editing the passage. */
const own = (e: Event) => e.stopPropagation();

/** The full path a file: link, or a bare absolute path, names on its host, or null for any other link. */
export function fileLinkPath(href: string): string | null {
  try {
    // A bare `/Users/…` is a file on the host, as on the Mac (#2754); `//elsewhere/…` is not.
    const url = href.startsWith("/") ? new URL(href, "file:///") : new URL(href);
    if (url.protocol !== "file:" || (url.host !== "" && url.host !== "localhost")) return null;
    const path = decodeURIComponent(url.pathname);
    return path.startsWith("/") ? path : null;
  } catch {
    return null;
  }
}

/** Whether a link may be followed from the page. */
export function isSafeLink(href: string): boolean {
  try {
    const url = new URL(href);
    return url.protocol === "http:" || url.protocol === "https:" || url.protocol === "mailto:";
  } catch {
    return false;
  }
}

const blockTags: Record<string, string> = {
  paragraph: "p", bullet_list: "ul", ordered_list: "ol", list_item: "li", blockquote: "blockquote",
  table: "table", thead: "thead", tbody: "tbody", tr: "tr", th: "th", td: "td",
  strong: "strong", em: "em", s: "s",
};

interface Frame {
  tag: string;
  props: Record<string, unknown>;
  children: ComponentChildren[];
}

/** Builds nodes from a flat token list with nesting, as markdown-it hands them over. */
function build(tokens: readonly Token[], images?: PageImages, openFile?: OpenFile, forgeImages?: ForgeImages, links?: PageLinks): ComponentChildren[] {
  const root: Frame = { tag: "", props: {}, children: [] };
  const stack: Frame[] = [root];
  const top = () => stack[stack.length - 1]!;
  const push = (node: ComponentChildren) => top().children.push(node);

  for (const token of tokens) {
    if (token.nesting === 1) {
      const name = token.type.replace(/_open$/, "");
      let tag = blockTags[name] ?? (name === "heading" ? token.tag : name === "link" ? "a" : "span");
      const props: Record<string, unknown> = {};
      if (name === "link") {
        const href = String(token.attrGet("href") ?? "");
        const path = openFile ? fileLinkPath(href) : null;
        const agent = agentLinkID(href);
        const linked = !path && links ? pageFile(href, links) : null;
        const file = linked && (links?.takes?.(linked) ?? true) ? linked : null;
        if (isSafeLink(href)) {
          Object.assign(props, { href, target: "_blank", rel: "noopener noreferrer" });
        } else if (agent) {
          tag = "button";
          Object.assign(props, { type: "button", class: "link reading", title: "Show the session", onClick: () => openAgentLink(agent) });
        } else if (path && openFile) {
          tag = "button";
          Object.assign(props, { type: "button", class: "link reading", title: path, onClick: () => openFile({ path }) });
        } else if (file && links) {
          tag = "button";
          Object.assign(props, { type: "button", class: "link reading", title: file, onKeyDown: own,
            onClick: (e: Event) => { own(e); links.open(file); } });
        } else {
          tag = "span";
        }
      }
      if (name === "ordered_list") {
        // No attribute is a list from 1; Number(null) would make it 0.
        const written = token.attrGet("start");
        const start = written === null ? 1 : Number(written);
        if (Number.isInteger(start) && start !== 1) props["start"] = start;
      }
      // A list item's paragraphs are tight in a tight list: markdown-it marks them hidden.
      if (name === "paragraph" && token.hidden) tag = "";
      stack.push({ tag, props, children: [] });
      continue;
    }
    if (token.nesting === -1) {
      const frame = stack.pop()!;
      if (frame.tag === "") top().children.push(...frame.children);
      // A table wraps its long cells to fit the reply, and scrolls sideways in its own box
      // only when it is too wide at its least, as on the Mac (#3895, #3943).
      else if (frame.tag === "table") push(h("div", { class: "table-scroll" }, h("table", frame.props, ...frame.children)));
      else push(h(frame.tag, frame.props, ...frame.children));
      continue;
    }
    switch (token.type) {
      case "inline": {
        // A task list item (`- [ ]`, `- [x]`): a box drawn and never ticked, as MarkdownText's (#252).
        const item = stack[stack.length - 2];
        const first = token.children?.[0];
        const box = item?.tag === "li" && item.children.length === 0 && top().children.length === 0
          && first?.type === "text" ? /^\[([ xX])\]\s+/.exec(first.content) : null;
        let children = token.children ?? [];
        if (box && first) {
          const done = box[1] !== " ";
          // A copy without the box: the tokens are kept and drawn again (#6328), so they are not changed.
          children = [Object.assign(Object.create(first) as Token, { content: first.content.slice(box[0].length) }), ...children.slice(1)];
          item!.props["class"] = "task";
          push(h("span", { class: `task-box${done ? " done" : ""}`, role: "img", "aria-label": done ? "Done" : "Not done" },
            done ? "☑" : "☐"));
          push(" ");
        }
        // A cell of more than a few words may wrap, but not under 12em, as the Mac's TableColumns (#3943).
        if ((top().tag === "td" || top().tag === "th") && token.content.length > 16) top().props["class"] = "long";
        top().children.push(...build(children, images, openFile, forgeImages, links));
        break;
      }
      case "text":
        push(token.content);
        break;
      case "softbreak":
        push("\n");
        break;
      case "hardbreak":
        push(h("br", null));
        break;
      case "code_inline":
        push(h("code", null, token.content));
        break;
      case "code_block":
      case "fence": {
        const language = token.info.trim().split(/\s+/)[0];
        const code = h("pre", language ? { "data-language": language } : null,
          h("code", null, language ? highlightCode(token.content, language) : token.content));
        // A Mermaid fence is its diagram, drawn from our own layout (#853); its code when it does not read.
        push(token.type === "fence" && isFence(language) ? h(MermaidFence, { text: token.content, code }) : code);
        break;
      }
      case "hr":
        push(h("hr", null));
        break;
      case "image": {
        const alt = token.children?.map((child) => child.content).join("") || String(token.attrGet("alt") ?? "");
        const src = String(token.attrGet("src") ?? "");
        // Beside the document, and only there. A chat has no document, so every image stays
        // a placeholder, and a remote one does even on a page (FR-031), but on a Work page,
        // whose host reads it with forge/image (#4011).
        const path = images ? pageImagePath(src, images.document) : null;
        const url = forgeImages ? forgeImageURL(src) : null;
        // An image that is a link's words (a badge, a logo) follows the link and does not open large (#4954).
        const linked = stack.some((frame) => frame.tag === "a" || frame.props["class"] === "link reading");
        if (path && images) {
          push(h(LocalPicture, { path, alt, revision: images.revision, read: images.read, linked }));
        } else if (url && forgeImages) {
          push(h(ForgePicture, { url, alt, read: forgeImages, linked }));
        } else {
          push(h("span", { class: "image-placeholder", title: src }, alt ? `[image: ${alt}]` : "[image]"));
        }
        break;
      }
      default:
        // Anything else (html_inline and html_block cannot occur with html: false) as its text.
        if (token.content) push(token.content);
    }
  }
  return root.children;
}

/**
 * Markdown as Preact nodes. `images` draws pictures from beside a document, and `forgeImages` a
 * forge page's; without them, none are fetched. `openFile` makes a file: link a way into that
 * file, and `links` a page's relative links too; without them, the link is its words.
 */
export function renderMarkdown(source: string, images?: PageImages, openFile?: OpenFile, forgeImages?: ForgeImages, links?: PageLinks): ComponentChildren[] {
  return build(parser.parse(source, {}), images, openFile, forgeImages, links);
}

const fenceOpen = /^ {0,3}(`{3,}|~{3,})/;
const listMarker = /^([-+*]|\d{1,9}[.)])(\s|$)/;
/** A link reference is the whole text's: one anywhere and the text is one block. */
const linkReference = /^ {0,3}\[[^\]]+\]:/m;
/** A table's delimiter row at the left edge, such as `| --- | :-: |`. */
const delimiterRow = /^\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$/;
/** A table is cut before a row once its part is this long (#6328), as on the Mac (#6299). */
const tablePart = 2_000;

/**
 * A part of a text (`markdownParts`): its words, and, for a part cut inside a table, the table's
 * header and delimiter rows, parsed before them so the part reads as a table too (#6328).
 */
export interface MarkdownPart {
  text: string;
  head?: string;
}

/** Where the cutting stood at a cut: the line before was blank, and the table it is in. */
interface CutState {
  blank: boolean;
  table: { head: string; rows: boolean } | null;
  head: string | undefined;
}

/** The parts of the text last cut, all but the last finished: a longer text starting with them is cut from there. */
let lastSplit: { done: string; parts: MarkdownPart[]; linked: boolean; state: CutState } =
  { done: "", parts: [], linked: false, state: { blank: false, table: null, head: undefined } };

/**
 * Markdown cut where it parses the same in pieces as whole (#214): before a line that starts at
 * the margin after a blank one, outside a fence, and that is no list item or quote, so no list,
 * quote, indented code or paragraph runs across the cut. Every part but the last is finished: a
 * message growing chunk by chunk parses only its last part again.
 *
 * A long table has no blank line, so it is also cut before a row (#6328, as #6299 on the Mac): a
 * table at the left edge after a blank line, whose rows each start with `|`, once the part is
 * `tablePart` long. The part after the cut gets the table's head, and `Markdown` draws its rows
 * in the table before it.
 *
 * A cut is remembered only once the line it cuts before has ended. Until then a longer line may
 * become a list item ("2" becoming "2. item"), and keeping the cut would parse differently from
 * the whole text.
 */
export function markdownParts(source: string): MarkdownPart[] {
  const continues = lastSplit.done !== "" && source.startsWith(lastSplit.done);
  // A link reference is resolved from anywhere, so once one is in the text it stays one block.
  if (lastSplit.linked && continues) return [{ text: source }];
  if (linkReference.test(continues ? source.slice(lastSplit.done.length) : source)) {
    lastSplit = { done: source, parts: [], linked: true, state: { blank: false, table: null, head: undefined } };
    return [{ text: source }];
  }
  const parts: MarkdownPart[] = continues ? [...lastSplit.parts] : [];
  let start = continues ? lastSplit.done.length : 0;
  let remembered = start;
  const rememberedParts = [...parts];
  let { blank, table, head } = continues ? lastSplit.state : { blank: false, table: null, head: undefined } as CutState;
  let rememberedState: CutState = { blank, table, head };
  // The line before, when it could be a table's header row: at the left edge after a blank line.
  let header: string | null = null;
  let fence: string | null = null;
  let at = start;
  const part = (text: string): MarkdownPart => head === undefined ? { text } : { text, head };
  const cut = (ended: boolean) => {
    parts.push(part(source.slice(start, at)));
    start = at;
    if (ended) {
      rememberedParts.push(parts[parts.length - 1]!);
      remembered = at;
    }
  };
  while (at < source.length) {
    const end = source.indexOf("\n", at);
    const next = end < 0 ? source.length : end + 1;
    const line = source.slice(at, end < 0 ? source.length : end);
    const ended = end >= 0;
    if (fence) {
      if (ended && line.trimStart().startsWith(fence) && line.trim().replaceAll(fence[0]!, "") === "" && line.length - line.trimStart().length < 4) fence = null;
    } else {
      const isBlank = line.trim() === "";
      if (table) {
        if (line.startsWith("|")) {
          if (table.rows && at - start >= tablePart) {
            cut(ended);
            head = table.head;
            if (ended) rememberedState = { blank: false, table, head };
          }
          table = { head: table.head, rows: true };
        } else {
          table = null;
        }
      } else if (header !== null && ended && delimiterRow.test(line)) {
        table = { head: `${header}\n${line}\n`, rows: false };
      }
      if (blank && !isBlank && at > start && !/^\s/.test(line) && !line.startsWith(">") && !listMarker.test(line)) {
        cut(ended);
        head = undefined;
        if (ended) rememberedState = { blank, table: null, head };
      }
      header = (blank || at === 0) && ended && line.startsWith("|") ? line : null;
      blank = isBlank;
      const opened = fenceOpen.exec(line);
      if (opened) fence = opened[1]!;
    }
    at = next;
  }
  lastSplit = { done: source.slice(0, remembered), parts: rememberedParts, linked: false, state: rememberedState };
  parts.push(part(source.slice(start)));
  return parts;
}

/** The texts of `markdownParts`, without the heads: joined, they are the source. */
export function markdownBlocks(source: string): string[] {
  return markdownParts(source).map((part) => part.text);
}

/** Each part's tokens, parsed once while it is drawn (#6328): a growing reply parses its last part. */
const parsedParts = new Map<string, Token[]>();
const parsedLimit = 256;
/** The characters parsed for parts, for a test to bound (#6328). */
export const markdownParsed = { characters: 0 };

function parsed(source: string): Token[] {
  let tokens = parsedParts.get(source);
  if (tokens) {
    // Kept as the newest, so a long reply's parts stay while older replies' go.
    parsedParts.delete(source);
  } else {
    tokens = parser.parse(source, {});
    markdownParsed.characters += source.length;
    if (parsedParts.size >= parsedLimit) parsedParts.delete(parsedParts.keys().next().value!);
  }
  parsedParts.set(source, tokens);
  return tokens;
}

/** The index after the top-level close of the block that opens at `from`. */
function closeOf(tokens: readonly Token[], from: number): number {
  let depth = 0;
  for (let at = from; at < tokens.length; at++) {
    depth += tokens[at]!.nesting;
    if (depth === 0) return at + 1;
  }
  return tokens.length;
}

/** A part's place in a table cut by rows: what of its tokens it draws. */
type TablePiece = "first" | "rows" | "after";

/** Whether a part reads as the table it is cut from: it starts with that table, and only its last part has more after it. */
function readsAsTable(source: string, last: boolean): boolean {
  const tokens = parsed(source);
  if (tokens[0]?.type !== "table_open") return false;
  return last || closeOf(tokens, 0) === tokens.length;
}

/** The tokens of a part that a piece draws: the first part's table inside, a later part's body rows, or what follows the table. */
function pieceTokens(tokens: readonly Token[], piece: TablePiece): readonly Token[] {
  const close = closeOf(tokens, 0);
  if (piece === "after") return tokens.slice(close);
  if (piece === "first") return tokens.slice(1, close - 1);
  const body = tokens.findIndex((token, at) => at < close && token.type === "tbody_open");
  return body < 0 ? [] : tokens.slice(body, closeOf(tokens, body));
}

interface DrawProps {
  images?: PageImages | undefined; openFile?: OpenFile | undefined; forgeImages?: ForgeImages | undefined; links?: PageLinks | undefined;
}

/** One finished block, parsed once. */
const MarkdownBlock = memo(function MarkdownBlock({ text, images, openFile, forgeImages, links }: DrawProps & { text: string }): ComponentChildren {
  return h(Fragment, null, ...build(parsed(text), images, openFile, forgeImages, links));
});

/** A piece of a table cut by rows, drawn again only when its part changes (#6328). */
const TablePart = memo(function TablePart({ source, piece, images, openFile, forgeImages, links }: DrawProps & { source: string; piece: TablePiece }): ComponentChildren {
  return h(Fragment, null, ...build(pieceTokens(parsed(source), piece), images, openFile, forgeImages, links));
});

/**
 * A table cut by rows, drawn as one table so its columns line up (#6328): the first part's head and
 * rows, then each later part's rows as a body of its own, then what follows the table. When a part
 * does not read as the same table, the parts are drawn as one block, as before the cut.
 */
function MarkdownTable({ parts, ...draw }: DrawProps & { parts: MarkdownPart[] }): ComponentChildren {
  const sources = parts.map((part) => (part.head ?? "") + part.text);
  if (!sources.every((source, index) => readsAsTable(source, index === sources.length - 1))) {
    return h(MarkdownBlock, { text: parts.map((part) => part.text).join(""), ...draw });
  }
  const last = sources[sources.length - 1]!;
  return h(Fragment, null,
    h("div", { class: "table-scroll" }, h("table", null,
      h(TablePart, { source: sources[0]!, piece: "first", ...draw }),
      ...sources.slice(1).map((source, index) => h(TablePart, { key: index, source, piece: "rows", ...draw })))),
    h(TablePart, { source: last, piece: "after", ...draw }));
}

/** A message's text, drawn; only a part whose text changed is parsed again (#170, #214, #6328). */
export const Markdown = memo(function Markdown({ text, ...draw }: DrawProps & { text: string }): ComponentChildren {
  const parts = markdownParts(text);
  const nodes: ComponentChildren[] = [];
  for (let index = 0; index < parts.length;) {
    let end = index + 1;
    while (parts[end]?.head !== undefined) end++;
    nodes.push(end > index + 1
      ? h(MarkdownTable, { key: `t${index}`, parts: parts.slice(index, end), ...draw })
      : h(MarkdownBlock, { key: index, text: parts[index]!.text, ...draw }));
    index = end;
  }
  return h("div", { class: "markdown" }, ...nodes);
});
