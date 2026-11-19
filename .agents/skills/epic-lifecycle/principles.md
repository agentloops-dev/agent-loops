# Principles for every epic

These principles apply at every stage of the `epic-lifecycle` skill. They are most
important at stage 3 (the data model) and stage 6 (the build). An output agrees with
them, or it says which principle it leaves out and why.

They add to `docs/explanation/direction.md` and AGENTS.md. They do not replace them.

## The client cache: local first

The aim is the feel of Slack or Linear: a screen never waits on the network.

- **Each client keeps a local cache.** The client brings data from the backend into the
  cache and works against the cache. A screen reads from the cache only.
- **Optimistic updates.** The client changes the cache at once. It does not wait for
  the server. The server then sends the value of record, which replaces the optimistic
  value. When the update fails, the client reverts the change and tells the person.
- **A normalized cache with list references.** Keep each record once, by its ID. A list
  keeps the IDs and their order, not copies of the records. Then one update to a record
  shows everywhere. When a refreshed list drops an item, replace the list and keep the
  record. This is the entity cache of Apollo Client and Relay.
- **Stale while revalidate.** Show the cached data at once. Fetch fresh data in the
  background, and update the screen when it comes. For example, show the cached list of
  Jira issues, then refresh the list, and update the cached list and each cached issue.
- **A last-updated time on each record.** Each cached record keeps the time of its last
  update. Ignore a late server response that is older than the client's copy. This
  keeps an optimistic edit safe from stale data.
- **The same cache shape on each client.** The storage is different, but the behavior
  is the same:
  - the Swift apps (Mac, iPhone, iPad) use a SQLite database;
  - the web page (React, served by Go) uses IndexedDB. Do not use local storage: it is
    small, synchronous and holds strings only. SQLite compiled to WebAssembly is
    possible if a shared schema and shared queries become valuable.

The SQLite and IndexedDB stores are client caches only. A cache can always be deleted
and filled again from the backend. The backend's store does not change (below).

## Storage on the backend

- The app is multi-tenant. First-party data is in files on disk, which an S3 bucket can
  hold: the control plane store of `docs/explanation/direction.md`, "Where data lives".
  For example, one file holds the list of agent sessions.
- Data from a third party, such as GitHub, is read live from that party and kept in the
  client cache. Do not copy it into the backend's store.
- Files are enough for now, but the store can change later. Keep each read behind a
  storage interface. Do not think that the data stays in files for ever.

## The data model

- **Base it on research.** Use open source projects and other literature. Do not make
  it up from nothing. Name the sources.
- **Multi-tenant from the start.** Each record belongs to a tenant
  (`docs/explanation/direction.md`, "Tenancy").
- **Bounded size.** Set a maximum size for each record, a maximum number of records,
  or both, so that the worst case is known. A collection with no natural limit gets a
  cap, pages, or a retention window. Fix slowness by bounding the data, not by adding
  concurrency.
- **Roll-ups for longer periods.** When data goes out of its retention window, keep a
  small roll-up (for example, daily or monthly totals). Then the long views still
  work without each raw record.
- **A soft cap of 90 days on raw data.** As a rule, keep no raw data older than 90
  days. This is a soft default and a design goal. It decreases regulatory exposure,
  but it does not keep the product outside rules such as GDPR, which depend on whether
  the data is personal data. A roll-up must be a true aggregate, with no personal data.
  Where it matters, ask the compliance owner (`docs/explanation/compliance/`).

## The API

- **RPC.** Each API is RPC, not REST and not GraphQL. Add methods to
  `Packages/Wire` (AGENTS.md, "Web/dist and generated.ts").
- **Pages for lists that can grow.** A list that can grow large has pages, so the size
  of each response has a limit. A small list with a fixed, known limit needs no pages.
  A cursor or an offset is satisfactory, when the response size has a limit.
- **Forward and backward compatible.** A new server works with an old client, and an
  old server works with a new client. Add optional fields. Do not remove or change the
  meaning of a field.

## UX and design

The cache principles make each screen feel instant. Design each screen to read from
the cache.

- **The look of each platform.** Each app follows the conventions of its platform:
  Liquid Glass on Apple platforms, and standard web patterns on the web. Do not make
  all platforms identical.
- **Loading.** A screen almost never shows a blocking spinner. Show the cached data at
  once, with a small indicator while it refreshes.
- **Empty states.** Each list or screen has an empty state. It says what goes there and
  gives the next action, so that an empty screen does not look broken.
- **A failed optimistic update.** When the server refuses a change and the client
  reverts it, tell the person what occurred. Where possible, let them try again. Never
  revert with no message.
- **Freshness.** A page that shows cached data says "Last update <time ago>"
  (`docs/reference/design-system.md`, "How fresh the data is").

## The build

- **A feature flag for each person, in Settings.** Build each new feature behind a
  flag that each person can turn on or off in Settings (#6254). Alex uses it to
  iterate on the Mac before the port. It needs no other process.

## Tests

- **Fast, and safe in parallel.** Unit tests run quickly and can run side by side.
- **No sleeps and no waits for a time.** Wait for a condition or an event, or use a
  fake clock. Never wait a fixed time: tests with sleeps were not reliable.

## The performance review

Keep the first two in mind from stage 3.

- **Complexity.** Find the order of complexity (big O) of each data path that the
  feature adds.
- **N plus one queries.** Look for one query that causes one more query for each item.
  This is most important for a third party that limits its rate, such as GitHub: a
  recent rate limit incident came from queries that did not count their calls. Use
  batched requests and the client cache. Do not make a call for each item.
- **Start from the logs.** The logs record the queries of each feature. Read the
  queries that a screen causes: how many, and how long each takes. Do not rely only on
  separate benchmarks.
