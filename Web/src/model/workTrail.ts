// A Work page's trail (#5452), as WorkTrail has it on the window and the Remote:
// Issues › Epic #5281 › #5291 for an issue in a feature's epic, Issues › #12 for one in none.
import type { LineageNode } from "../protocol/generated";
import { type WorkKind, shortRef, workKindTitle } from "./work";

/** Where a part opens: a Work list, or an item's page (the epic). */
export type TrailTarget =
  | { kind: "list"; section: WorkKind }
  | { kind: "item"; section: WorkKind; ref: string };

/** A part of the trail; the last, the page itself, has no target. */
export type TrailPart = { words: string; target?: TrailTarget };

/** The trail of an item's page. feature and parent are the item's, from its read: a page starts at its kind's list; an
 * issue with a parent has it before itself, as Epic #n when the issue has a feature (an epic's children take its
 * feature: label, #3271). */
export function workTrail(kind: WorkKind, ref: string, feature?: string, parent?: LineageNode): TrailPart[] {
  const parts: TrailPart[] = [{ words: workKindTitle[kind], target: { kind: "list", section: kind } }];
  const f = feature || undefined;
  if (kind === "issue" && parent?.ref && parent.ref !== ref) {
    const short = shortRef(parent.ref);
    parts.push({ words: f ? `Epic ${short}` : short, target: { kind: "item", section: "issue", ref: parent.ref } });
  }
  parts.push({ words: shortRef(ref) });
  return parts;
}

/** The tip of a part: what it opens. */
export function trailHelp(part: TrailPart): string | undefined {
  const t = part.target;
  if (!t) return undefined;
  if (t.kind === "list") return `All ${workKindTitle[t.section].toLowerCase()}`;
  return `The page of ${part.words}`;
}
