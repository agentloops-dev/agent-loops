// A browser's key-value store with a limit (#6669, docs/explanation/data-stores.md, #7253): every write of localStorage and
// sessionStorage goes through putEntry, so each entry has a largest size, and `scripts/check-store-bounds.py` sees each store's
// declaration at its call. It refuses and never evicts: an entry stays until it is removed. A refused write changes nothing
// that is kept; what it was for holds while the page is open.

const encoder = new TextEncoder();

/** Whether `text` takes at most `most` bytes of UTF-8. */
export function fits(text: string, most: number): boolean {
  return text.length <= most && (text.length * 3 <= most || encoder.encode(text).length <= most);
}

/** Puts `value` under `key` when it is at most `most` bytes; whether the store took it. A store that is full or off takes nothing. */
export function putEntry(storage: Pick<Storage, "setItem"> | undefined, key: string, value: string, most: number): boolean {
  if (!storage || !fits(value, most)) return false;
  try {
    storage.setItem(key, value); // store-bounds: not a store: each caller of putEntry declares its own
    return true;
  } catch {
    return false;
  }
}

/** The names that are kept, at most `count` of `bytes` each: the ones there first stay. */
export function boundedNames(names: Iterable<string>, count: number, bytes: number): string[] {
  const kept: string[] = [];
  for (const name of names) {
    if (kept.length >= count) break;
    if (fits(name, bytes)) kept.push(name);
  }
  return kept;
}

/** The most bytes of a list of names as JSON: an escape takes at most six bytes for one. */
export function namesBytes(count: number, bytes: number): number {
  return 2 + count * (6 * bytes + 3);
}
