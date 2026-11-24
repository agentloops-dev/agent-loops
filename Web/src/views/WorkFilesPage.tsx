// The Files changed page (#5477, specs/pr-files-changed-page/spec.md), pushed in a pull request's place on its Work
// page, as the Checks page is: ‹ #4402 (or Escape) back; Files changed with the counts, Unified | Split on a wide page,
// Collapse all and On GitHub; the file list with a filter beside the diff on a page 700 px wide or more, Files ▾ (the
// same list in a sheet) on a narrower one; a card per file, its header sticky, a click on it collapses the card. The
// lines are FileDiff's, in the diff colours (#5476). It reads forge/diff when it opens. The Mac's is #5478.
import { useSignal } from "@preact/signals";
import { useEffect, useMemo, useRef } from "preact/hooks";
import type { ForgeItemDetail } from "../protocol/generated";
import type { Store } from "../model/store";
import { describe } from "../model/errors";
import { hunkRows } from "../model/fileDiff";
import {
  cardPath, changeWord, collapseWords, couldNotRead, cutWords, filesCounts, filesCountsSpoken, listWidth, matchFiles, nameAndFolder, splitWidth,
} from "../model/filesPage";
import { forgeName, shortRef } from "../model/work";
import type { DiffFile } from "./chat/appViewBridge";
import { FileDiff, type DiffLayout } from "./FileDiff";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import { OpenGlyph } from "./Work";
import { WorkFooter } from "./WorkParts";
import { pageFooterState } from "../model/workFooter";

/** The Review page's key, so the choice is the same on both pages. */
const layoutKey = "reviewDiffLayout";

interface Read { files: DiffFile[]; cut: boolean; url?: string | undefined }

export function WorkFilesPage({ store, host, detail, back }: { store: Store; host: string; detail: ForgeItemDetail; back: () => void }) {
  const node = detail.item.node;
  const ref = shortRef(node.ref);
  const site = forgeName(node.ref);
  const read = useSignal<Read | null>(null);
  const problem = useSignal<string | null>(null);
  const asked = useSignal(0);
  const reading = useSignal(false);
  const layout = useSignal<DiffLayout>(globalThis.localStorage?.getItem(layoutKey) === "sideBySide" ? "sideBySide" : "unified");
  const collapsed = useSignal<ReadonlySet<number>>(new Set());
  const filter = useSignal("");
  const picking = useSignal(false);
  const width = useSignal(typeof innerWidth === "number" ? innerWidth : 1280);
  const page = useRef<HTMLElement>(null);

  useEffect(() => {
    let current = true;
    problem.value = null;
    reading.value = true;
    store.link.call("forge/diff", { node }, host).then(
      (d) => { if (current) read.value = { files: d.files, cut: d.cut, url: d.url }; },
      (e) => { if (current) problem.value = describe(e); })
      .finally(() => { if (current) reading.value = false; });
    return () => { current = false; };
  }, [asked.value]);

  useEffect(() => {
    const el = page.current;
    const measure = () => {
      if (!el) return;
      width.value = el.clientWidth;
      if (width.value >= listWidth) picking.value = false;
    };
    measure();
    const seen = typeof ResizeObserver === "function" ? new ResizeObserver(measure) : null;
    if (el) seen?.observe(el);
    const onEscape = (e: KeyboardEvent) => { if (e.key === "Escape" && !picking.value && !e.defaultPrevented) back(); };
    addEventListener("keydown", onEscape);
    return () => { seen?.disconnect(); removeEventListener("keydown", onEscape); };
  }, []);

  const files = read.value?.files ?? [];
  const rows = useMemo(() => files.map((f) => hunkRows(f.hunks)), [read.value]);
  const shown = matchFiles(files, filter.value);
  const wide = width.value >= listWidth;
  const split = width.value >= splitWidth && layout.value === "sideBySide";
  const url = read.value?.url ?? (detail.item.url ? `${detail.item.url}/files` : undefined);

  const choose = (next: DiffLayout) => { layout.value = next; globalThis.localStorage?.setItem(layoutKey, next); };
  const toggle = (i: number) => {
    const next = new Set(collapsed.value);
    if (next.has(i)) next.delete(i); else next.add(i);
    collapsed.value = next;
  };
  const allCollapsed = files.length > 0 && collapsed.value.size >= files.length;
  const collapseAll = () => { collapsed.value = allCollapsed ? new Set() : new Set(files.map((_, i) => i)); };
  const go = (i: number) => {
    picking.value = false;
    if (collapsed.value.has(i)) toggle(i);
    requestAnimationFrame(() => page.current?.querySelector(`[data-file="${i}"]`)?.scrollIntoView({ block: "start" }));
  };

  const list = (
    <div class="files-list">
      <input type="search" placeholder="Filter files" aria-label="Filter files" value={filter.value}
        onInput={(e) => { filter.value = (e.target as HTMLInputElement).value; }} />
      {shown.length === 0 ? <p class="quiet small">No file matches</p> : (
        <ul>
          {shown.map((i) => {
            const f = files[i]!;
            const { name, folder } = nameAndFolder(f.path);
            const change = changeWord(f);
            return (
              <li key={f.path}>
                <button type="button" title={cardPath(f)} onClick={() => go(i)}>
                  <span class="words"><span class="name">{name}</span>{folder && <span class="folder quiet small">{folder}</span>}</span>
                  {change && <span class={`change ${change}`}>{change}</span>}
                  <span class="counts small" aria-label={`${f.additions} added, ${f.deletions} removed`}>
                    <span class="plus">+{f.additions}</span> <span class="minus">−{f.deletions}</span></span>
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );

  return (
    <section class={`work-files-page work-footed${wide ? " wide" : ""}`} aria-label="Files changed" ref={page}>
      <div class="work-page-bar">
        <nav class="work-place" aria-label="Place">
          <button type="button" class="link" onClick={back}><Icon name="chevron-left" />{ref}</button>
        </nav>
      </div>
      <header class="files-head">
        <h2>Files changed</h2>
        {read.value && <span class="quiet small" aria-label={filesCountsSpoken(files)}>{filesCounts(files)}</span>}
        <span class="files-tools">
        {!wide && files.length > 0 && (
          <button type="button" class="files-pick" aria-haspopup="dialog" onClick={() => { picking.value = true; }}>
            Files<Icon name="chevron-down" /></button>
        )}
        {width.value >= splitWidth && files.length > 0 && (
          <span class="tabs files-layout" role="tablist" aria-label="Show">
            <button type="button" role="tab" aria-selected={layout.value === "unified"} onClick={() => choose("unified")}>Unified</button>
            <button type="button" role="tab" aria-selected={layout.value === "sideBySide"} onClick={() => choose("sideBySide")}>Split</button>
          </span>
        )}
        {files.length > 0 && <button type="button" onClick={collapseAll}>{collapseWords(files.length, collapsed.value.size)}</button>}
        {url && <a class="files-open-on" href={url} target="_blank" rel="noopener noreferrer"><OpenGlyph />On {site}</a>}
        </span>
      </header>
      {/* The read's state is in the footer (#5971, the Mac's #5970); the diff is not kept, so it says no age. */}
      {problem.value && !read.value ? (
        <div class="work-could-not" role="alert">
          <span class="mark" aria-hidden="true"><Icon name="triangle-alert" /></span>
          <p class="strong">{couldNotRead(ref)}</p>
          <p class="quiet">{problem.value}</p>
          <p class="buttons">
            <button type="button" onClick={() => { asked.value++; }}>Try again</button>
            {url && <a href={url} target="_blank" rel="noopener noreferrer">On {site}</a>}
          </p>
        </div>
      ) : !read.value ? (
        <div class="files-body" aria-busy="true" aria-label="Loading">
          {wide && <div class="files-list placeholder"><span class="bar" /><span class="bar" /><span class="bar short" /></div>}
          <div class="files-cards placeholder">
            {[0, 1, 2].map((i) => <div key={i} class="files-card"><div class="files-card-head"><span class="bar" /></div><div class="files-card-lines" /></div>)}
          </div>
        </div>
      ) : files.length === 0 ? (
        <p class="files-empty quiet">No files changed.</p>
      ) : (
        <div class="files-body">
          {wide && <aside class="files-side" aria-label="Files">{list}</aside>}
          <div class="files-cards">
            {read.value.cut && <p class="quiet small">{cutWords(site)}</p>}
            {files.map((f, i) => {
              const open = !collapsed.value.has(i);
              const change = changeWord(f);
              return (
                <section key={f.path} class={`files-card${open ? "" : " collapsed"}`} data-file={i} aria-label={cardPath(f)}>
                  <button type="button" class="files-card-head" aria-expanded={open} onClick={() => toggle(i)}
                    title={open ? "Collapse this file" : "Show this file"}>
                    <Icon name={open ? "chevron-down" : "chevron-right"} />
                    <span class="path">{cardPath(f)}</span>
                    {change && <span class={`change ${change}`}>{change}</span>}
                    <span class="spacer" />
                    <span class="counts small" aria-label={`${f.additions} lines added, ${f.deletions} removed`}>
                      <span class="plus">+{f.additions}</span> <span class="minus">−{f.deletions}</span></span>
                  </button>
                  {open && (rows[i]?.length
                    ? <FileDiff rows={rows[i]!} layout={split ? "sideBySide" : "unified"} />
                    : <p class="files-note quiet small">{f.note ?? "No lines to show."}</p>)}
                </section>
              );
            })}
          </div>
        </div>
      )}
      <WorkFooter state={pageFooterState(read.value !== null, reading.value, false, problem.value ? [problem.value] : [])}
        retry={() => { asked.value++; }} />
      {picking.value && !wide && (
        <Modal label="Files" close={() => { picking.value = false; }}>
          <div class="files-sheet">
            <header><h2>Files</h2><span class="spacer" />
              <button type="button" class="prominent" onClick={() => { picking.value = false; }}>Done</button></header>
            {list}
          </div>
        </Modal>
      )}
    </section>
  );
}
