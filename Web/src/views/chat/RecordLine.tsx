// The one Line (#5036; specs/session-cards/spec.md, family 2): the record of what happened, with no box and no glyph
// (#5675): a bold head, a quiet detail and at most one action link at the end. When it has more to show, a click on the
// words opens it in place, under the words, and a small chevron says so. Red only on a failure's words
// (#5016): the head, or the detail of a line with no head. Tool lines, thoughts, the plan line, notes, notified, a
// report, the answered Ask's "You chose", and the sandbox and policy records are all drawn with it. The window and
// the Remote draw the same in Shared/UI/Chat/RecordLine.swift.
import { useSignal } from "@preact/signals";
import type { ComponentChildren, JSX } from "preact";
import { useEffect } from "preact/hooks";
import { Icon } from "../Icon";

export interface LineAction {
  label: string;
  title?: string;
  disabled?: boolean;
  run: () => void;
}

export function RecordLine({ lead, head, detail, failure = false, oneLine = false, action, onClick, clickTitle,
  openTitles = ["Show more", "Show less"], focused = false, extra, more }: {
  /** Plain words before the head: "You chose". */
  lead?: string | undefined;
  /** In bold; none for a line that is all detail (a tool line, a thought). */
  head?: string | undefined;
  /** In the quiet colour, after the head. */
  detail?: string | undefined;
  failure?: boolean;
  /** One line, cut at the end: a tool line, a thought. Anything else wraps. */
  oneLine?: boolean;
  /** The one action link at the end of the line: See Policy, Try Again, Steps. */
  action?: LineAction | undefined;
  /** What a click on the words does instead of opening: a folded run unfolds. */
  onClick?: (() => void) | undefined;
  clickTitle?: string | undefined;
  /** What the click says it does, closed and open. */
  openTitles?: [string, string];
  /** Gone to from elsewhere, so opened (#1315). */
  focused?: boolean;
  extra?: string;
  /** What opens under the words; none for a line with nothing more. */
  more?: ComponentChildren;
}): JSX.Element {
  const open = useSignal(false);
  useEffect(() => {
    if (focused && more !== undefined) open.value = true;
  }, [focused]);
  const opens = more !== undefined && more !== null && more !== false;
  const clicks = opens || onClick !== undefined;
  const hasHead = head !== undefined || lead !== undefined;
  const words = (
    <>
      <span class={`record-line-words${oneLine ? " one-line" : ""}`}>
        {lead !== undefined && <span class="record-line-lead">{lead} </span>}
        {head !== undefined && <span class="record-line-head">{head}</span>}
        {detail ? <span class={`record-line-detail${failure && !hasHead ? " failed" : ""}`}>{hasHead ? ` · ${detail}` : detail}</span> : null}
      </span>
      {clicks && <Icon name="chevron-right" class={`record-line-chevron${open.value ? " open" : ""}`} />}
    </>
  );
  return (
    <div class={`record-line${failure ? " failure" : ""}${extra ? ` ${extra}` : ""}`}>
      <div class="record-line-row">
        {onClick
          ? <button type="button" class="record-line-open" title={clickTitle} onClick={onClick}>{words}</button>
          : opens
            ? <button type="button" class="record-line-open" aria-expanded={open.value} title={open.value ? openTitles[1] : openTitles[0]}
              onClick={() => (open.value = !open.value)}>{words}</button>
            : <span class="record-line-open">{words}</span>}
        {action && <button type="button" class="link record-line-action" title={action.title} disabled={action.disabled}
          onClick={action.run}>{action.label}</button>}
      </div>
      {opens && open.value && <div class="record-line-more">{more}</div>}
    </div>
  );
}
