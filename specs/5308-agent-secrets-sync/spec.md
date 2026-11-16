---
description: A secret that an agent saves on a machine becomes the person's and goes to every machine, sealed by the person's own client; the control plane never opens it and a machine never seals to another machine (#5308).
---

# An agent's secret becomes yours

Issue #5308, a child of epic #5254. It builds on the client sealing of #3397
(`specs/3397-client-sealing/spec.md`) and keeps all of its principles.

## Decisions

Alex, 2026-11-15, by a form on #5308:

1. **Which saves.** These saves sync: ask_secret, capture_secret, generate_secret (and their
   `_PREVIOUS` copies), and the sign-up passwords. Passkeys and the secrets typed in a
   connector's form on a machine stay On This Machine.
2. **Old saves.** At its next connection, a machine offers the agent-saved secrets that it
   already has. The rules are the same as for a new save.

Decided: the Keep Yours / Use the Agent's choice is a call from the person's own client to
the control plane, never an answer to a machine, because a machine taken over could answer
its own card. Alex can overrule.

Decided: the Remote's card has no buttons. It does not seal, so it cannot do Use the Agent's,
and Keep Yours alone would offer one side of the choice; its card says to choose on the Mac or
in the browser. Alex can overrule.

Decided: the machine's envelopes have no sender authentication (HPKE base mode, as in
#3397). A control plane that has the clients' keys could make an "agent's secret". But it
can already seal a value straight to a machine today, so this gives it no new reach. Alex
can overrule.

## The steps

1. An agent saves a secret on a machine. The machine keeps it in `sealed.json`, as before,
   with `from: "agent"`. Then it asks for the person's clients (`machine/secretClients`),
   seals the value to each one, and gives the envelopes to the control plane
   (`machine/offerSecret`). This is an **offer**. The machine seals to clients only, never to
   another machine.
2. The control plane keeps the offer, writes an audit entry (the name, the machine and the
   session, never the value) and tells the person's clients (`control/sealingWanted`).
3. A Mac window or a web page of the person's that is open calls `user/secretOffers`. For
   each offer that has no conflict, it opens its own envelope and sets the secret as Add a
   Secret does (`user/setSecret`, sealed to every machine and client, with the same Ask
   before use and description), with `offer` set. The control plane then removes the offer
   and keeps where the secret came from (`savedBy`). The Remote shows the rows and does not
   seal.
4. The machine's next `machine/userSettings` no longer names the offer in `offersWaiting`.
   The person's copy replaces the machine's own copy in `sealed.json`.

Until step 3, the offer is a row in Needs you: "Saved by an agent on devbox. It goes to
your other machines when you next open Agents."

## A name that is already yours

The offer does not replace the person's secret. Both are kept: the person's secret, and
the offer at the control plane. Needs you shows the offer with **Keep Yours**
(`user/dropSecretOffer`) and **Use the Agent's** (step 3, at the kept version plus one). The
other machines keep the person's value until the person chooses. The machine that saved
the secret keeps using its agent's value while the offer waits (its delivery names the
offer in `offersWaiting`). After the choice, the person's copy replaces it.

The control plane holds this rule itself: over a name that the person has, `user/setSecret`
takes an offer only at the kept version plus one (Use the Agent's, from the version the person
saw). An app that takes it as a new name is refused. Use the Agent's keeps the person's
description when the agent gave none. An offer's id is a UUID, as `user/setSecret` takes it;
`machine/offerSecret` refuses any other.

## The envelope

- Info: `com.alexecollins.agents.agentsecret.v1`.
- Associated data, as UTF-8:
  `agentsecret.v1|<person id>|<machine id>|<recipient>|<NAME>|<offer id>`, with `|ask` at
  the end when Ask before use is on.

The machine id is bound, so a control plane cannot show an offer as one from another
machine without making a new one (see the second decision).

## What the control plane keeps

`v1/users/<user>/offers/<machine>.<NAME>.json`: `{offer, name, machine, machineName, agent,
savedAt, needsApproval, description, sealed: {<client recipient>: envelope}}`. One offer per
machine and name: a newer one replaces it. At most 100 offers per person, 200 recipients
per offer, and 4 MiB per record. A removed machine's offers go with it.

## The methods

| Method | Who | What |
|---|---|---|
| `machine/secretClients` | a machine | The person's clients that have a key, and the person's id. |
| `machine/offerSecret` | a machine | An offer. Its envelopes are for the person's clients only. |
| `user/secretOffers` | the person | The waiting offers, each with this client's envelope and `conflict`. |
| `user/setSecret {…, offer}` | the person | As before; `offer` takes the offer. |
| `user/dropSecretOffer {offer}` | the person | Keep Yours: removes the offer. |
| `machine/userSettings {…, offersWaiting}` | to a machine | This machine's offers that wait. |

`user/secretNames` rows get `savedBy {machine, name, agent, at}`: "Saved by an agent on
devbox, 2 h ago".

## What does not sync

- Secrets For a Session. They end with their session, on one machine.
- Passkeys and connector-form secrets (decision 1).
- A name the control plane does not take (capital letters, digits and `_`, up to 64).
- A secret that the machine cannot open again. On Linux, a value with Ask before use opens
  only with a release that a person approved (#4058). The machine seals it when the agent
  saves it. When that offer fails, the secret stays On This Machine.

An old save (decision 2) is an entry with no `from` that no MCP server names (a connector's
form) and that is not a passkey.

## Principles (direction.md)

- **6 (holds nothing it could misuse):** the control plane keeps only envelopes that it
  cannot open, and it never seals. A machine only receives from other machines: it seals
  to the person's clients, and only a client seals to the machines.
- **1 (nothing relies on the Mac):** the web page takes offers too.

None is left out. The known limit of #3397 (a client recipient's key is not in the lock)
stays.
