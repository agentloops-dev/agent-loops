/** A client store of the **cache** kind (#6669, docs/explanation/data-stores.md), as the Mac's CacheStore (#6788): data this
 * device can delete and read again. It evicts; it never refuses a write because it is full. A miss is always a valid answer. */
export interface CacheStore {
  /** Brings the store under its bounds: the age first, then the size. The store runs it each hour itself. */
  sweep(): Promise<void>;
  /** What the store holds now, for its health. */
  stats(): Promise<CacheStats>;
  /** Forgets everything held; the next read fills it again. */
  forget(): void;
}

/** The size of a cache: never what it holds. */
export interface CacheStats {
  count: number;
  bytes: number;
  /** When the oldest record was read from its source; undefined when the cache is empty. */
  oldest?: number | undefined;
}

/** The longest that any cache keeps a record: 90 days (#6669, the time rule). */
export const cacheAgeLimit = 90 * 24 * 60 * 60 * 1000;
