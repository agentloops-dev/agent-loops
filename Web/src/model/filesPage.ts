// The Files changed page's words and its list (#5477, specs/pr-files-changed-page/spec.md): the counts in the
// header, a file list row's name and folder, the filter, and Collapse all. WorkFilesPage draws them.

/** What the page needs of a file: its path, the path before a rename, its change and its counts. */
export interface ChangedFile { path: string; previousPath?: string | undefined; change: string; additions: number; deletions: number }

/** The header's counts: "6 files · +412 −18". */
export function filesCounts(files: readonly ChangedFile[]): string {
  const added = files.reduce((n, f) => n + f.additions, 0), removed = files.reduce((n, f) => n + f.deletions, 0);
  return `${files.length} ${files.length === 1 ? "file" : "files"} · +${added} −${removed}`;
}

/** The same, said: "6 files, 412 lines added, 18 removed". */
export function filesCountsSpoken(files: readonly ChangedFile[]): string {
  const added = files.reduce((n, f) => n + f.additions, 0), removed = files.reduce((n, f) => n + f.deletions, 0);
  return `${files.length} ${files.length === 1 ? "file" : "files"}, ${added} lines added, ${removed} removed`;
}

/** A list row's name (the last part of the path) and its folder (the rest, empty at the root). */
export function nameAndFolder(path: string): { name: string; folder: string } {
  const at = path.lastIndexOf("/");
  return at < 0 ? { name: path, folder: "" } : { name: path.slice(at + 1), folder: path.slice(0, at) };
}

/** A card's path: "old → new" for a rename. */
export const cardPath = (f: ChangedFile) => f.previousPath && f.previousPath !== f.path ? `${f.previousPath} → ${f.path}` : f.path;

/** The change's word beside the path, or none for a modified file. */
export const changeWord = (f: ChangedFile) => f.change === "modified" ? undefined : f.change;

/** The indices of the files the filter matches: every word of it in the path (or the old path), any case. */
export function matchFiles(files: readonly ChangedFile[], filter: string): number[] {
  const words = filter.toLowerCase().split(/\s+/).filter(Boolean);
  const out: number[] = [];
  files.forEach((f, i) => {
    const hay = `${f.previousPath ?? ""} ${f.path}`.toLowerCase();
    if (words.every((w) => hay.includes(w))) out.push(i);
  });
  return out;
}

/** The header's toggle: Expand all when every card is collapsed, else Collapse all. */
export const collapseWords = (count: number, collapsed: number) => count > 0 && collapsed >= count ? "Expand all" : "Collapse all";

/** The error's title. */
export const couldNotRead = (ref: string) => `Could not read the files of ${ref}`;

/** The cut line, for a diff the forge gave only in part. */
export const cutWords = (site: string) => `The diff is too long to show every file here. A file with no lines is on ${site}.`;

/** The widths: the list beside the diff from this page width, and Split from this one. */
export const listWidth = 700;
export const splitWidth = 820;
