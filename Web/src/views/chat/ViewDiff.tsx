// A view's agents/open-diff (#1341): a pull request's files over the chat or the pin, drawn with
// the Review page's diff (FileDiff, #1472), not in the view. Each file's hunks under its path and
// counts; long unchanged runs folded, a break between hunks. Unified, or Side by Side on a wide
// page. Done or Escape closes it. The Mac's and the Remote's is Shared/UI/AppView/AppViewDiffPage.swift.
import { useSignal } from "@preact/signals";
import { useEffect, useMemo, useRef } from "preact/hooks";
import { hunkRows } from "../../model/fileDiff";
import { chooseDiffLayout, diffLayout } from "../../model/diffLayout";
import { FileDiff } from "../FileDiff";
import type { ViewLayer } from "./viewLayer";

/** What ViewDiff draws: a layer's diff, or a Work page's (#3130). */
type DiffHolder = Pick<ViewLayer, "diff">;

export function ViewDiff({ layer, openLink }: { layer: DiffHolder; openLink: (url: string) => void }) {
  const diff = layer.diff.value;
  const layout = diffLayout();
  const wide = useSignal(typeof innerWidth === "number" && innerWidth >= 820);
  const body = useRef<HTMLDivElement>(null);
  const rows = useMemo(() => (diff?.files ?? []).map((f) => hunkRows(f.hunks)), [diff]);

  useEffect(() => {
    if (!diff) return;
    const close = (e: KeyboardEvent) => { if (e.key === "Escape") { e.preventDefault(); layer.diff.value = null; } };
    const size = () => { wide.value = innerWidth >= 820; };
    size();
    addEventListener("keydown", close);
    addEventListener("resize", size);
    if (diff.path) body.current?.querySelector(`[data-path="${CSS.escape(diff.path)}"]`)?.scrollIntoView({ block: "start" });
    return () => { removeEventListener("keydown", close); removeEventListener("resize", size); };
  }, [diff]);

  if (!diff) return null;
  const added = diff.files.reduce((n, f) => n + f.additions, 0), removed = diff.files.reduce((n, f) => n + f.deletions, 0);
  return (
    <div class="view-diff" role="dialog" aria-modal="true" aria-label={diff.title}>
      <header class="column-head">
        <h1>{diff.title}</h1>
        <span class="spacer" />
        {wide.value && (
          <span class="tabs" role="tablist" aria-label="Show">
            <button type="button" role="tab" class={layout === "unified" ? "on" : ""} aria-selected={layout === "unified"} onClick={() => chooseDiffLayout("unified")}>Unified</button>
            <button type="button" role="tab" class={layout === "sideBySide" ? "on" : ""} aria-selected={layout === "sideBySide"} onClick={() => chooseDiffLayout("sideBySide")}>Side by Side</button>
          </span>
        )}
        <button type="button" class="prominent" onClick={() => { layer.diff.value = null; }}>Done</button>
      </header>
      <div class="scroll" ref={body}>
        <div class="review-body">
          <div class="review-diff-head">
            <span>{diff.files.length} {diff.files.length === 1 ? "file" : "files"} changed</span>
            <span class="quiet small counts" aria-label={`${added} lines added, ${removed} removed`}>+{added} −{removed}</span>
            <span class="spacer" />
            {diff.url && <button type="button" onClick={() => openLink(diff.url!)}>Open on {diff.site}</button>}
          </div>
          {diff.cut && <p class="quiet small">The diff is too long to show every file here. A file with no lines below is on {diff.site}.</p>}
          {diff.files.map((f, i) => (
            <section class="view-diff-file" data-path={f.path} key={f.path}>
              <div class="review-diff-head">
                <strong class="small path">{f.previousPath ? `${f.previousPath} → ${f.path}` : f.path}</strong>
                {f.change !== "modified" && <span class="quiet small">{f.change}</span>}
                <span class="spacer" />
                <span class="quiet small counts" aria-label={`${f.additions} lines added, ${f.deletions} removed`}>+{f.additions} −{f.deletions}</span>
              </div>
              {rows[i]?.length ? <FileDiff rows={rows[i]!} layout={wide.value ? layout : "unified"} />
                : <p class="quiet small">{f.note ?? "No lines to show."}</p>}
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
