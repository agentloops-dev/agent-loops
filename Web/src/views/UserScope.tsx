// The person's secrets and cards (#1760, #3632), by name only, which go to every machine of theirs: the sheets, Reveal
// and Copy, and the read, as the window's (Shared/UI/UserScope/SecretSheets.swift). The Secrets page (#5257) draws them.
import { useEffect, useRef } from "preact/hooks";
import { useSignal } from "@preact/signals";
import type { Store } from "../model/store";
import { Failure, type UserSecretKind, type UserSecretNames, type UserSecretRow } from "../protocol/generated";
import { Capability } from "../protocol/capabilities.generated";
import { CallFailed } from "../wire/link";
import { openSealedSecret, sealSecret } from "../wire/sealing";
import { describe, methodNotFound } from "../model/errors";
import { type Card, cardAddressHeader, cardAddressHint, cardConfirm, cardKind, type CardPart, cardParts, cardProblem, cardRule, cardValue, clipboardSeconds, copiedSeconds,
  copyRefused, describedAs, didNotOpen, isSecretName, nameHint, nextVersion, noCopyYet, parseCard, partLabels, revealNeedsNewerPlane,
  shownSeconds, spacedNumber, tidyCard } from "../model/userScope";
import { AskBeforeUseCheck } from "./AskBeforeUse";
import { Modal } from "./Modal";
import { SecretDescriptionField } from "./SecretDescription";

export function sentence(error: unknown): string {
  if (error instanceof CallFailed && error.code === methodNotFound) return "This control plane does not keep settings for you yet. Update it.";
  return describe(error);
}

/** Seals the value here to each of the person's machines and apps (#3397), and sends only the envelopes: the
 * control plane never has it. kind is "card" for a card (#3531). Ask before use (#4016) is sealed into every
 * copy's associated data, so it is set here, on Add and Replace: no client can read a value back to seal it again. */
export async function setSecret(store: Store, name: string, value: string, replacing: UserSecretRow | null, ask: boolean,
  description: string, kind?: UserSecretKind) {
  const version = nextVersion(replacing, Date.now());
  const { recipients, user } = await store.link.call("user/secretRecipients", {});
  // Only to the machines the lock names (#3491, D8): the control plane cannot add a key of its own.
  const values = await sealSecret(user ?? "", name, version, value, await store.signedRecipients(recipients), ask);
  await store.link.call("user/setSecret", { name: name as never, version, values, ...(kind ? { kind } : {}),
    ...(ask ? { needsApproval: true } : {}), ...describedAs(description) });
}

export function SecretSheet({ store, replacing, close, saved, suggested }: { store: Store; replacing: UserSecretRow | null; close: () => void;
  /** After a save, with the name: the Secrets page selects it (#5257). */
  saved?: (name: string) => void;
  /** The name to start with: Save as a Secret Instead… on the Environment page (#5260). */
  suggested?: string }) {
  const name = useSignal(replacing?.name ?? suggested?.toUpperCase() ?? "");
  const value = useSignal("");
  // A new secret starts on (#4162); a replaced one keeps its own.
  const ask = useSignal(replacing ? replacing.needsApproval === true : true);
  const description = useSignal(replacing?.description ?? "");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const save = async () => {
    busy.value = true;
    try {
      await setSecret(store, name.value.trim(), value.value, replacing, ask.value, description.value);
      value.value = "";
      saved?.(name.value.trim());
      close();
    } catch (error) {
      problem.value = sentence(error);
    } finally {
      busy.value = false;
    }
  };
  const chosen = name.value.trim();
  return (
    <Modal label={replacing ? `Replace ${replacing.name}` : "Add a Secret"} close={close}>
      <form method="dialog" class="sheet-body" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <h2>{replacing ? `Replace ${replacing.name}` : "Add a Secret"}</h2>
        {!replacing && (
          <input type="text" aria-label="Name" placeholder="GITHUB_TOKEN" autofocus autocomplete="off" spellcheck={false}
            value={name.value} onInput={(e) => (name.value = (e.currentTarget as HTMLInputElement).value.toUpperCase())} />
        )}
        <input type="password" aria-label="Value" placeholder="Value" autocomplete="off" value={value.value}
          onInput={(e) => (value.value = (e.currentTarget as HTMLInputElement).value)} />
        <SecretDescriptionField text={description} />
        <AskBeforeUseCheck on={ask} />
        <p class={problem.value ? "sheet-note failure" : "sheet-note"} role={problem.value ? "alert" : undefined}>{problem.value ?? nameHint}</p>
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={busy.value || !value.value || !isSecretName(chosen)}>{replacing ? "Replace" : "Add"}</button>
        </div>
      </form>
    </Modal>
  );
}

/** Add a Card… and a card's Replace… (#3531): only a card with a spend limit (a merchant or category lock is recommended). */
export function CardSheet({ store, replacing, close, saved }: { store: Store; replacing: UserSecretRow | null; close: () => void;
  /** After a save, with the name: the Secrets page selects it (#5257). */
  saved?: (name: string) => void }) {
  const name = useSignal(replacing?.name ?? "");
  const number = useSignal("");
  const expiry = useSignal("");
  const cvc = useSignal("");
  const holder = useSignal("");
  const zip = useSignal("");
  // The billing address (#5569), optional.
  const line1 = useSignal("");
  const line2 = useSignal("");
  const city = useSignal("");
  const region = useSignal("");
  const country = useSignal("");
  const locked = useSignal(false);
  // A new card starts on (#4162); a replaced one keeps its own.
  const ask = useSignal(replacing ? replacing.needsApproval === true : true);
  const description = useSignal(replacing?.description ?? "");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const save = async () => {
    const card = tidyCard({ number: number.value, expiry: expiry.value, cvc: cvc.value, name: holder.value, zip: zip.value,
      line1: line1.value, line2: line2.value, city: city.value, state: region.value, country: country.value });
    const wrong = cardProblem(card);
    if (wrong) {
      problem.value = wrong;
      return;
    }
    busy.value = true;
    try {
      await setSecret(store, name.value.trim(), cardValue(card), replacing, ask.value, description.value, cardKind);
      number.value = "";
      cvc.value = "";
      line1.value = "";
      line2.value = "";
      saved?.(name.value.trim());
      close();
    } catch (error) {
      problem.value = sentence(error);
    } finally {
      busy.value = false;
    }
  };
  const field = (label: string, signal: { value: string }, props: Record<string, unknown> = {}) => (
    <input type="text" aria-label={label} placeholder={label} autocomplete="off" spellcheck={false} value={signal.value}
      onInput={(e) => (signal.value = (e.currentTarget as HTMLInputElement).value)} {...props} />
  );
  const title = replacing ? `Replace ${replacing.name}` : "Add a Card";
  return (
    <Modal label={title} close={close}>
      <form method="dialog" class="sheet-body" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <h2>{title}</h2>
        <p class="sheet-note">{cardRule}</p>
        <label>
          <input type="checkbox" checked={locked.value} onChange={(e) => (locked.value = (e.currentTarget as HTMLInputElement).checked)} />
          {" "}{cardConfirm}
        </label>
        {!replacing && (
          <input type="text" aria-label="Name" placeholder="SHOP_CARD" autocomplete="off" spellcheck={false}
            value={name.value} onInput={(e) => (name.value = (e.currentTarget as HTMLInputElement).value.toUpperCase())} />
        )}
        {field("Card number", number, { type: "password", inputMode: "numeric" })}
        {field("Expiry (MM/YY)", expiry, { inputMode: "numeric" })}
        {field("CVC", cvc, { type: "password", inputMode: "numeric" })}
        {field("Name on card", holder)}
        {field("Billing ZIP", zip)}
        <fieldset class="card-address">
          <legend>{cardAddressHeader}</legend>
          {field("Address line 1", line1)}
          {field("Address line 2", line2)}
          {field("City", city)}
          {field("State", region)}
          {field("Country", country)}
          <p class="sheet-note">{cardAddressHint}</p>
        </fieldset>
        <SecretDescriptionField text={description} />
        <AskBeforeUseCheck on={ask} />
        <p class={problem.value ? "sheet-note failure" : "sheet-note"} role={problem.value ? "alert" : undefined}>{problem.value ?? nameHint}</p>
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent"
            disabled={busy.value || !locked.value || !number.value || !cvc.value || !isSecretName(name.value.trim())}>
            {replacing ? "Replace" : "Add"}
          </button>
        </div>
      </form>
    </Modal>
  );
}

/** A value shown (#5115), and when it hides. */
export interface Shown { name: string; version: number; value: string; card: Card | null; until: number }
/** What was just copied: the row, and a card's part. */
interface Copied { name: string; part: CardPart | null }

/**
 * Reveal and Copy (#5112, #5115; specs/5112-view-copy-secrets-accounts/wireframes.md, sections 1 to 6), as the window's
 * SecretShowState: the page asks the control plane for its own sealed copy (user/sealedSecret, which writes the audit
 * entry first), opens it with its own key, and shows or copies the value. One value at a time, for 30 s, in memory
 * only; it hides when the tab is hidden, the section closes, or the secret changes.
 */
export function useSecretShow(store: Store, rows: UserSecretRow[]) {
  const shown = useSignal<Shown | null>(null);
  const copied = useSignal<Copied | null>(null);
  const fetching = useSignal<string | null>(null);
  const problems = useSignal<Record<string, string>>({});
  const noCopy = useSignal<Set<string>>(new Set());
  const now = useSignal(Date.now());
  const person = useRef<string | null>(null);
  const timers = useRef<{ hide?: number; copied?: number; tick?: number }>({});
  const can = store.planeHas(Capability.user4);

  const hide = () => {
    window.clearTimeout(timers.current.hide);
    window.clearInterval(timers.current.tick);
    shown.value = null;
  };
  useEffect(() => {
    const hidden = () => { if (document.visibilityState === "hidden") hide(); };
    document.addEventListener("visibilitychange", hidden);
    return () => {
      document.removeEventListener("visibilitychange", hidden);
      hide();
      window.clearTimeout(timers.current.copied);
    };
  }, []);
  // A value shown hides when its secret changed or went; a replaced secret is sealed again, to this page too.
  useEffect(() => {
    const s = shown.value;
    if (s && rows.find((r) => r.name === s.name)?.version !== s.version) hide();
    const kept = [...noCopy.value].filter((name) => rows.some((r) => r.name === name));
    if (kept.length !== noCopy.value.size) noCopy.value = new Set(kept);
  }, [rows]);

  const say = (name: string, problem: string | null) => {
    const next = { ...problems.value };
    if (problem) next[name] = problem; else delete next[name];
    problems.value = next;
  };
  const sealedValue = async (row: UserSecretRow, purpose: "reveal" | "copy", part: CardPart | null): Promise<string | null> => {
    const record = await store.link.keyRecord();
    if (!record) {
      say(row.name, noCopyYet);
      return null;
    }
    fetching.value = row.name;
    say(row.name, null);
    try {
      person.current ??= (await store.link.call("user/secretRecipients", {})).user ?? "";
      const sealed = await store.link.call("user/sealedSecret", { name: row.name as never, purpose, ...(part ? { part } : {}) });
      try {
        return await openSealedSecret(record, person.current, sealed);
      } catch {
        say(row.name, didNotOpen);
        return null;
      }
    } catch (error) {
      if (error instanceof CallFailed && error.code === Failure.noClientCopy) noCopy.value = new Set([...noCopy.value, row.name]);
      else say(row.name, error instanceof CallFailed && error.code === methodNotFound ? revealNeedsNewerPlane : sentence(error));
      return null;
    } finally {
      fetching.value = null;
    }
  };

  const toggle = async (row: UserSecretRow) => {
    if (shown.value?.name === row.name) {
      hide();
      return;
    }
    hide();
    const value = await sealedValue(row, "reveal", null);
    if (value === null) return;
    shown.value = { name: row.name, version: row.version ?? 0, value, card: row.kind === cardKind ? parseCard(value) : null,
      until: Date.now() + shownSeconds * 1000 };
    now.value = Date.now();
    timers.current.tick = window.setInterval(() => { now.value = Date.now(); }, 1000);
    timers.current.hide = window.setTimeout(hide, shownSeconds * 1000);
  };

  const copy = async (row: UserSecretRow, part: CardPart | null = null) => {
    const card = row.kind === cardKind;
    const which = part ?? (card ? "number" : null);
    const value = await sealedValue(row, "copy", which);
    if (value === null) return;
    const parsed = card ? parseCard(value) : null;
    if (card && !parsed) {
      say(row.name, didNotOpen);
      return;
    }
    const text = parsed && which ? parsed[which] : value;
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      say(row.name, copyRefused);
      return;
    }
    // A browser lets a page write the clipboard only while its tab is in front, so the clear is only then.
    window.setTimeout(() => {
      if (document.visibilityState === "visible" && document.hasFocus()) void navigator.clipboard.writeText("").catch(() => undefined);
    }, clipboardSeconds * 1000);
    copied.value = { name: row.name, part: shown.value?.name === row.name ? part : null };
    window.clearTimeout(timers.current.copied);
    timers.current.copied = window.setTimeout(() => { copied.value = null; }, copiedSeconds * 1000);
  };

  return { can, shown, copied, fetching, problems, noCopy, now, toggle, copy };
}

export type SecretShow = ReturnType<typeof useSecretShow>;

/** The value shown, in a well that a person cannot select: Copy is the only way to copy it, so each copy is audited. */
export function ShownValue({ row, shown, show }: { row: UserSecretRow; shown: Shown; show: SecretShow }) {
  if (!shown.card) return <code class="secret-shown" aria-label={`${row.name} value`}>{shown.value}</code>;
  const card = shown.card;
  return (
    <div class="secret-shown" aria-label={`${row.name} parts`}>
      {cardParts.map((part) => {
        const done = show.copied.value?.name === row.name && show.copied.value.part === part;
        return (
          <div class="card-part" key={part}>
            <span class="part-label">{partLabels[part]}</span>
            <span class="part-value">{part === "number" ? spacedNumber(card.number) : card[part]}</span>
            <button type="button" aria-label={`Copy ${partLabels[part]} of ${row.name}`} onClick={() => void show.copy(row, part)}>
              {done ? "Copied ✓" : "Copy"}
            </button>
          </div>
        );
      })}
    </div>
  );
}

/** What the control plane keeps for the person, read again on each control/userSettingsChanged and on each
 * connection. */
export function useUserScope(store: Store, down: boolean) {
  const names = useSignal<UserSecretNames | null>(null);
  const problem = useSignal<string | null>(null);
  /** When the names were read (#5146), for the "Last update" line. */
  const readAt = useSignal<number | undefined>(undefined);
  const load = async () => {
    try {
      names.value = await store.link.call("user/secretNames", {});
      readAt.value = Date.now();
      problem.value = null;
    } catch (error) {
      problem.value = sentence(error);
    }
  };
  const changes = store.userScopeChanges.value;
  useEffect(() => { if (!down) void load(); }, [changes, down]);
  return { names, problem, load, readAt };
}
