---
name: epic-lifecycle
description: Take a feature from a missing behavior that Alex finds to every client its tier names, as design, MVP and port epics (#6253, #7037) - ideate, a data model with its access patterns (gate 1), low- and high-fidelity HTML wireframes (gate 2, the UX review), a build on the Mac behind a per-person feature flag, iteration, a performance and usability review with realistic data, then the port to the other clients. Load principles.md at the data model and build stages. Use when Alex starts a new epic, asks to ideate a product area, asks for a data model or wireframes, or asks how an epic goes from idea to all clients.
---

# Epic lifecycle

This skill is the lifecycle of one feature, from a missing behavior to all clients. It is light and iterative. It is not a spec-driven waterfall: do not use the
`speckit-*` skills for it.

- **Each stage makes one small output.** Make it in one session when you can.
- **Two stages are gates:** the data model (stage 3) and the UX review (stage 5). Alex
  approves each. All other stages flow without a stop.
- **Epics build up from the bottom.** An epic usually sits on a layer that an earlier
  epic settled. Name that layer in the epic body.
- **The principles are in `principles.md`,** beside this file. Read it at stage 3 and
  at stage 6. Each output agrees with it, or says which principle it leaves out and why.
- **The output goes in the forge, not in a file** (#5312). Write it in the epic body or
  in a comment on the epic. Put a long part in a `<details>` block.

## Which epics (#7037)

| Epic | Stages | When |
|---|---|---|
| **Design** | 1 to 5 | Only when Alex chooses it; else its children are the first children of the MVP epic |
| **MVP** (or proof of concept) | 6 to 8, and 1 to 5 by default | Always; one client, usually the Mac |
| **Port** | 9 | When the tier names more clients and the port is more than one issue |

- **Design is in the MVP epic by default.** At stage 2, ask Alex with `ask_question`
  whether to make it a separate epic, done before the MVP epic. That epic and each of
  its children get `priority:high`: a priority does not pass from an epic to its
  children. It gets no lead.
- **Each design output has an issue:** one child (`spike`) for the data model (gate 1)
  and one for the wireframes (gate 2), which is blocked by the first. The build children
  are blocked by the design children. The output itself is a comment on the epic that
  has the design children. A design issue closes on Alex's approval at its gate, not
  before: that is what the build waits for.
- **Each epic is "blocked by" the one before it:** MVP by design, port by MVP.
- **A proof of concept is not thrown away.** It can become the MVP. When it merges, it
  follows the normal rules: gate 2, the flag, and Alex's approval of a new screen.
- **A port of one issue gets no epic.** It goes to the standing epic #4208: file it with
  no parent, "blocked by" the MVP epic.
- **A follow-up at the end of an epic** does not hold the epic open: the `lead` skill,
  "Keep the plan true", says where it goes.

## Who does each stage

| Stages | Who | Skill or persona |
|---|---|---|
| 1 to 5 | The Product Owner, with Alex | `.agents/personas/product-owner.md` |
| 6 to 8 | The MVP epic's lead, by its lanes | the `lead` skill |
| 9 | The port epic's lead, by its lanes (one port issue: the lead of #4208) | the `lead` skill |

The Product Owner files the epics and adds each design output. The MVP epic's lead
starts when gate 2 is approved. The port epic's lead starts when the MVP epic is closed.
With a separate design epic, the Product Owner closes it when its children are closed.

## Stage 1: Find what is missing

Alex uses the product and finds a behavior that is wrong or missing. That starts the
epic. Write it in one or two sentences, as Alex said it.

## Stage 2: Ideate

Talk the area through with Alex. Find how other products do it: search the web, and
read the competitor reports in `~/.agents/reviews/competitors/`. The output is a shared
understanding, not a document.

- File the epic (label `epic`, one `feature:` label, a priority). Look for a duplicate
  first.
- Write in its body: the missing behavior, what other products do (with links), and the
  options, the recommended one first.
- Give the feature a role and a tier (AGENTS.md, "Keeping the three clients in step").
  The tier says which clients stage 9 ports to.
- File the design children and ask Alex about a separate design epic ("Which epics",
  above). File the port with the plan: a port epic with a child for each client, or one
  port issue.

## Stage 3: Data model and access patterns (gate 1)

Read `principles.md` before you start. Draft the data model. Base it on open source
projects and other literature: name them. Iterate with Alex.

Add a short section, "Storage and access", of a few paragraphs. Answer each of these
questions:

1. What is the expected volume of each record type, and how fast does it grow?
2. How long does each record type live? What is its retention, and what roll-up keeps
   the long view?
3. What are the hot queries and read paths that the screens use?
4. If the data grows, how do you partition or shard it, and on what key?
5. How does each record type map to a real database later? Which storage interface
   keeps the reads behind it?
6. What is the maximum size of a record, and the maximum number of records?
7. Which calls go to a third party (GitHub, Jira…), how many for each screen, and how
   do you batch them?

Write the API too: the RPC methods, which lists have pages, and the new optional
fields.

**Gate 1:** ask Alex to approve the data model and access patterns with
`ask_question`. This is usually fast, often in the first session. Do not wait days:
when Alex changes it, change it and ask again. On approval, close the data model issue.

## Stage 4: Low-fidelity wireframes

Make a wireframe of each screen that sits over the data model. Use basic HTML, not
ASCII art or a text sketch. Use plain boxes, real labels, and sample data of a
realistic size. Keep it fast to make.

- Show each state: loaded, empty, refreshing, and a failed optimistic update.
- Show "Last update <time ago>" where the page shows cached data.
- Draw only the clients that the tier names.

Put the wireframes where Alex can see them ("Where wireframes go", below).

## Stage 5: High-fidelity wireframes (gate 2)

Make the same screens again, at high fidelity. Use the design tokens
(`design/tokens.json`, `docs/reference/design-system.md`) and the look of each
platform: Liquid Glass on the Mac, iPhone and iPad, and standard web patterns on the
web.

**Gate 2: Alex reviews the UX.** Never skip this gate. Ask with `ask_question`, with a
link to the pictures. When Alex asks for changes, change the wireframes and ask again.
On approval, close the wireframes issue: the build children are then ready.

## Stage 6: Build on the Mac, behind a feature flag

Read `principles.md` again. The lead files the children of the epic and starts their
lanes. Build the first version on one lead client, normally the Mac window.

- Put the feature behind a per-person feature flag that Settings can change (#6254).
  Until #6254 lands, the first epic that needs a flag builds it.
- Build from the bottom up: the store and the wire first, then the cache, then the
  screens.
- The UI rules of AGENTS.md still apply. A PR that adds a screen, control or layout
  waits for Alex. Its commit says what the other two clients do. While the flag is off
  on the other clients, write `remote: #NNN` and `web: #NNN` for the port issues of
  stage 9: the children of the port epic, or the one port issue.

## Stage 7: Test and iterate

Alex turns on the flag and uses the feature. Each change that Alex asks for is a small
child of the epic, with its own lane and PR. Continue until Alex says that the
experience is right on the Mac. An example of one change: a "Last update" time on
several pages.

## Stage 8: Performance and usability review

Do this review as a step of its own. A feature can be almost done and still not good
enough. In the worst case, a new feature makes the product unusable because it is
slow.

- Load realistic data volumes: the volumes of stage 3, not small test data.
- Read the logs first. Count the queries that each screen does, and how long each
  takes.
- Look for N plus one queries, mainly to a third party that limits its rate.
- Find the order of complexity (big O) of each data path.
- Follow the `performance-review` and `usability-check` skills for the method, but for
  this epic only.

File each finding as a child of the epic. Fix the critical and high ones before
stage 9. When this stage is done, the lead closes the MVP epic (the `lead` skill,
step 5): it does not wait for the port. A finding left for later is a follow-up.

## Stage 9: Port to the other clients (the port epic)

When the experience is settled on the Mac, port the feature to the clients that its
tier names: the web page, and the Remote on iPhone and iPad when the tier is Core.
Each port follows the same principles and has the same cache shape. Update the parity
table (`docs-parts/parity/`) in the same branch.

The flag stays until the port is done. Taking it out is the last child of the port epic,
blocked by the others, or a part of the one port issue. With no port, it is the last
child of the MVP epic. When all its children are closed, the lead closes the port epic.

## Where wireframes go

A wireframe is never committed to `main` (#5312). Do these steps:

1. Write the HTML in a scratch folder in `/tmp`.
2. Open it with the `browser_*` tools and take a screenshot of each screen and state.
3. Write the comment in a file: refer to each picture as `![…](./<screen>-<state>.png)`, and
   put the HTML source in a `<details>` block.
4. Post it on the epic with the pictures: the run-app skill's
   `scripts/pr-shots.sh comment <epic> <comment.md> <picture>...` (#6963). It pushes nothing.
5. Delete the scratch folder.

## Questions still open

- Does Alex want more principles for sync and conflicts, offline behavior, and
  incremental loading in the data model? (#6253)
