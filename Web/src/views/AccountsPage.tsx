// Status ▸ Accounts (#5258, specs/5254-secrets-accounts-environment/wireframes.md sections 3 and 5), as the window's
// AccountsPageView (Shared/UI/UserScope/AccountsPageView.swift): every outside account of the person's on one page, at
// #/accounts, and one selected at #/accounts/<id>. The accounts part of their settings (#3967), which goes to every
// machine of theirs; a record holds no secret, only the name of one in Secrets. The Secrets page's frame: a search
// (never a value), chips with counts, a Project and a Service menu and a sort over a list with Needs you on top and the
// total a month at the foot, and the selected account's detail beside it, where Edit turns its fields into fields in
// place. Under 720 px the list is alone. It replaces the read-only Status list (#4161), Settings ▸ You ▸ Accounts and its
// Details… sheet (#5116).
import { useEffect, useRef } from "preact/hooks";
import { useSignal } from "@preact/signals";
import type { JSONValue, OutsideAccount, OutsideAccounts, UserSealedSecretPart, UserSealedSecretPurpose, UserSecretRow } from "../protocol/generated";
import type { Store } from "../model/store";
import { go, route } from "../route";
import { CallFailed } from "../wire/link";
import { describe, methodNotFound } from "../model/errors";
import { type AccountFields, accountAbout, accountAskHint, accountAsks, accountFields, accountHint, accountLine, accountOf,
  accountSecretName, accountsRefusal, askField, noSecrets } from "../model/accounts";
import { accountID, accountName, type Chip, chips, chipTitles, counts as chipCounts, foot, groups as groupsOf, type Item,
  items as itemsOf, noMatch, planLine, projectsOf, servicesOf, shortCost, type Sort, sorts, sortTitles, subtitle, w } from "../model/accountsPage";
import { askBeforeUseWords } from "../model/askBeforeUse";
import { hint as secretHint, machineNames, onlyMachine, onlyMachineLine } from "../model/secretInfo";
import { auditAccount, type CardPart, cardParts, copiedSeconds, copyText, named, type Named, noSecretLine, notInSecrets, parseCard,
  revealFailure, revealSecret, revealWords, shownSeconds, tokenExpiry } from "../model/reveal";
import { Modal } from "./Modal";
import { SecretInfoLine } from "./SecretInfoLine";
import { LastUpdate } from "./LastUpdate";
import { ask } from "./MessageKinds";
import { Icon } from "./Icon";
import { useNarrow } from "./SecretsPage";

function sentence(error: unknown): string {
  if (error instanceof CallFailed && error.code === methodNotFound) return "This control plane does not keep settings for you yet. Update it.";
  return describe(error);
}

const FIELDS: [key: string, label: string, hint: string][] = [
  ["service", "Service", "gitlab.com"], ["address", "Address", "name@example.com"],
  ["username", "User name", "When it is not the address"],
  ["description", "Description", "What it is for, and who uses it"], ["plan", "Plan", "Free"],
  ["cost", "Monthly cost (USD)", "0"], ["card", "Card", "The card's name in Secrets"], ["secret", "Secret", "The token's name in Secrets"],
  ["twoFactor", "Two-factor", "TOTP, passkey, SMS"], ["issue", "Issue", "#12"], ["made", "Made", "YYYY-MM-DD"],
  ["tokenExpires", "Token expires", "YYYY-MM-DD"], ["projects", "Projects", "None is all, else names with commas"],
  ["notes", "Notes", "Where the password is"],
];

function open(id: string | null): void {
  go(id ? { nav: "status", section: "accounts", item: id } : { nav: "status", section: "accounts" });
}

/** The record's fields, for Add Account… and an Edit in place: each optional but the service. */
function FieldInputs({ fields, autofocus }: { fields: { value: AccountFields }; autofocus?: boolean }) {
  const asks = fields.value[askField] !== "off";
  return (
    <>
      <div class="account-fields">
        {FIELDS.map(([key, label, hint], i) => (
          <label key={key}>
            <span class="hint">{label}</span>
            <input type="text" aria-label={label} placeholder={hint} autocomplete="off" spellcheck={false}
              autofocus={autofocus && i === 0} value={fields.value[key] ?? ""}
              onInput={(e) => (fields.value = { ...fields.value, [key]: (e.currentTarget as HTMLInputElement).value })} />
          </label>
        ))}
      </div>
      <label>
        <input type="checkbox" checked={asks}
          onChange={(e) => (fields.value = { ...fields.value, [askField]: (e.currentTarget as HTMLInputElement).checked ? "" : "off" })} />
        {" "}{askBeforeUseWords.toggle}
      </label>
      <p class="sheet-note">{accountAskHint(asks)}</p>
    </>
  );
}

/** Add Account…: the record's fields. The new account is selected when it is saved (wireframes §2a). */
function AccountSheet({ save, close }: { save: (a: OutsideAccount) => Promise<string | null>; close: () => void }) {
  const fields = useSignal<AccountFields>({});
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const title = "Add an Account";
  const submit = async () => {
    busy.value = true;
    problem.value = await save(accountOf(fields.value));
    busy.value = false;
    if (!problem.value) close();
  };
  return (
    <Modal label={title} close={close}>
      <form method="dialog" class="sheet-body" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
        <h2>{title}</h2>
        <FieldInputs fields={fields} autofocus />
        <p class={problem.value ? "sheet-note failure" : "sheet-note"} role={problem.value ? "alert" : undefined}>{problem.value ?? noSecrets}</p>
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={busy.value || !(fields.value.service ?? "").trim()}>Add</button>
        </div>
      </form>
    </Modal>
  );
}

export function AccountsPage({ store, down }: { store: Store; down: boolean }) {
  const all = useSignal<OutsideAccount[]>([]);
  const secrets = useSignal<UserSecretRow[]>([]);
  const problem = useSignal<string | null>(null);
  /** When the accounts were read (#5146), for the "Last update" line. */
  const readAt = useSignal<number | undefined>(undefined);
  const search = useSignal("");
  const chip = useSignal<Chip>("all");
  const project = useSignal<string | null>(null);
  const service = useSignal<string | null>(null);
  const sort = useSignal<Sort>("service");
  const adding = useSignal(false);
  const searchField = useRef<HTMLInputElement>(null);
  const narrow = useNarrow();
  const load = async () => {
    try {
      const settings = await store.link.call("user/settings", {});
      all.value = (settings.accounts as OutsideAccounts | undefined)?.accounts ?? [];
      // The Secrets rows give each account's last 4 and whether its secret is backed up (#4160).
      secrets.value = await store.link.call("user/secretNames", {}).then((r) => r.secrets, () => []);
      readAt.value = Date.now();
      problem.value = null;
    } catch (error) {
      problem.value = sentence(error);
    }
  };
  const changes = store.userScopeChanges.value;
  useEffect(() => { if (!down) void load(); }, [changes, down]);
  // ⌘F (Ctrl+F) puts the cursor in the search, as the window's (wireframes §2).
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "f") {
        e.preventDefault();
        searchField.current?.focus();
      }
    };
    document.addEventListener("keydown", key);
    return () => document.removeEventListener("keydown", key);
  }, []);

  /** The accounts with replacing taken out and adding put in its place, set as the part. */
  const save = async (adding: OutsideAccount | null, replacing: OutsideAccount | null): Promise<string | null> => {
    const accounts = [...all.value];
    const at = replacing ? accounts.findIndex((a) => accountID(a) === accountID(replacing)) : -1;
    if (at >= 0) accounts.splice(at, 1);
    if (adding) accounts.splice(at >= 0 ? at : accounts.length, 0, adding);
    try {
      const value: OutsideAccounts = { accounts };
      await store.link.call("user/setSettings", { part: "accounts", value: value as unknown as JSONValue });
      await load();
      void store.loadUserScopeStatus();
      return null;
    } catch (error) {
      return sentence(error);
    }
  };

  const names = machineNames(store.hosts.value);
  const rows = secrets.value;
  const list = itemsOf(all.value, rows, names);
  const counts = chipCounts(list, search.value, project.value, service.value);
  const groups = groupsOf(list, search.value, chip.value, project.value, service.value, sort.value);
  const shown = groups.flatMap((g) => g.items);
  const selectedID = route.value.section === "accounts" ? route.value.item ?? null : null;
  const selected = list.find((i) => i.id === selectedID) ?? null;
  const only = onlyMachine(rows);
  // An older control plane refuses the accounts part, so the page offers none (#4100).
  const refusal = accountsRefusal(store.plane.value);

  const move = (step: number) => {
    if (shown.length === 0) return;
    const at = selected ? shown.findIndex((i) => i.id === selected.id) : -1;
    const next = shown[Math.max(0, Math.min(shown.length - 1, at + step))];
    if (next) open(next.id);
  };
  const listKeys = (e: KeyboardEvent) => {
    if (e.key === "ArrowDown") { e.preventDefault(); move(1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); move(-1); }
  };
  const menu = (label: string, any: string, value: { value: string | null }, options: string[]) =>
    (options.length > 0 || value.value !== null) && (
      <label class="select-label">{label}
        <select value={value.value ?? ""} onChange={(e) => { value.value = (e.target as HTMLSelectElement).value || null; }}>
          <option value="">{any}</option>
          {options.map((o) => <option key={o} value={o}>{o}</option>)}
        </select>
      </label>
    );

  const listPane = (
    <div class="secrets-list" role="listbox" aria-label="Accounts" tabIndex={0} onKeyDown={listKeys}>
      {problem.value && <p class="hint failure">{problem.value}</p>}
      {only && all.value.some((a) => accountSecretName(a) !== null) &&
        <p class="hint attention"><Icon name="triangle-alert" />{" "}{onlyMachineLine(only, names)}</p>}
      {list.length === 0 && readAt.value !== undefined && <p class="hint">{w.none}</p>}
      {list.length > 0 && groups.length === 0 && search.value && (
        <p class="hint">{noMatch(search.value)}{" "}
          <button type="button" class="link" onClick={() => { search.value = ""; }}>{w.clearSearch}</button>
        </p>
      )}
      {groups.map((group) => (
        <div key={group.id} class="secrets-group" role="group" aria-label={group.title ?? w.title}>
          {group.title && <h3 class="secrets-group-head">{group.title} · {group.items.length}</h3>}
          {group.items.map((item) => (
            <button key={item.id} type="button" role="option" aria-selected={selected?.id === item.id}
              class={`secrets-row${selected?.id === item.id ? " on" : ""}${item.needs ? " needs" : ""}`} onClick={() => open(item.id)}>
              <span class="secrets-row-name">
                {item.needs && <Icon name="triangle-alert" />}
                <span>{accountName(item.account)}</span>
              </span>
              <code class="secrets-row-hint">{accountHint(item.account, rows)}</code>
              <span class="secrets-row-age">{shortCost(item.account) ?? ""}</span>
              <span class={`secrets-row-sub${item.needs ? " warning" : ""}`}>{subtitle(item)}</span>
            </button>
          ))}
        </div>
      ))}
      <p class="hint small accounts-foot">{foot(shown, list.length)}</p>
    </div>
  );

  const detailPane = selected
    ? <AccountDetail key={selected.id} store={store} item={selected} rows={rows} down={down}
        save={async (changed) => {
          const failed = await save(changed, selected.account);
          if (!failed && accountID(changed) !== selected.id) open(accountID(changed));
          return failed;
        }}
        remove={async () => {
          if (!await ask(`Remove ${accountName(selected.account)}? ${w.removeMessage}`)) return;
          const failed = await save(null, selected.account);
          if (failed) store.say(failed); else open(null);
        }} />
    : (
      <div class="secrets-empty">
        <p class="hint">{w.selectOne}</p>
        {list.length > 0 && <p class="hint small">{foot(list, list.length)}</p>}
      </div>
    );

  return (
    <div class={`secrets-page${narrow ? " narrow" : ""}`}>
      <div class="secrets-tools" role="search">
        <input ref={searchField} type="search" aria-label="Search accounts" placeholder={w.searchPrompt} value={search.value}
          onInput={(e) => { search.value = (e.target as HTMLInputElement).value; }}
          onKeyDown={(e) => { if (e.key === "Escape") search.value = ""; else if (e.key === "ArrowDown") { e.preventDefault(); move(1); } }} />
        <button type="button" class="prominent" disabled={down || refusal !== null} onClick={() => { adding.value = true; }}>+ {w.add}</button>
        {readAt.value !== undefined && <LastUpdate at={readAt.value} />}
      </div>
      <div class="secrets-chips machine-chips" role="group" aria-label="Filters">
        {chips.map((c) => (
          <button key={c} type="button" class={`chip${chip.value === c ? " on" : ""}${c === "needsYou" ? " attention" : ""}`}
            aria-pressed={chip.value === c} onClick={() => { chip.value = chip.value === c && c !== "all" ? "all" : c; }}>
            {c === "needsYou" && <><Icon name="triangle-alert" />{" "}</>}{chipTitles[c]} {counts[c]}
          </button>
        ))}
        {menu("Project", w.projectAny, project, projectsOf(list))}
        {menu("Service", w.serviceAny, service, servicesOf(list))}
        <label class="select-label">{w.sortLabel}
          <select value={sort.value} onChange={(e) => { sort.value = (e.target as HTMLSelectElement).value as Sort; }}>
            {sorts.map((s) => <option key={s} value={s}>{sortTitles[s]}</option>)}
          </select>
        </label>
      </div>
      {refusal ? <p class="hint">{refusal}</p> : narrow ? (
        selected ? (
          <div class="secrets-detail-narrow">
            <button type="button" class="link" onClick={() => open(null)}>‹ {w.title}</button>
            {detailPane}
          </div>
        ) : listPane
      ) : (
        <div class="secrets-split">
          {listPane}
          <div class="secrets-detail">{detailPane}</div>
        </div>
      )}
      {adding.value && (
        <AccountSheet close={() => { adding.value = false; }}
          save={async (a) => {
            const failed = await save(a, null);
            if (!failed) {
              search.value = "";
              chip.value = "all";
              project.value = null;
              service.value = null;
              open(accountID(a));
            }
            return failed;
          }} />
      )}
    </div>
  );
}

/** The detail of one account (wireframes §3): what Details… showed (#5116), beside the list. Each field, Copy for each
 * plain one; the secret and the card it names, with Reveal and Copy and → to open it in Secrets; Ask before use as a
 * switch, saved at once (an account is not sealed); the age line; Edit, which turns the fields into fields in place, and
 * Remove…. A reveal or a copy of a value goes in the audit log; a copy of a plain field does not. */
function AccountDetail({ store, item, rows, down, save, remove }: {
  store: Store; item: Item; rows: UserSecretRow[]; down: boolean;
  save: (a: OutsideAccount) => Promise<string | null>; remove: () => Promise<void>;
}) {
  const account = item.account;
  const editing = useSignal(false);
  const fields = useSignal<AccountFields>(accountFields(account));
  const problem = useSignal<string | null>(null);
  const saving = useSignal(false);
  // The one value shown (one at a time), by its slot, "secret" or "card".
  const shown = useSignal<{ slot: string; value: string } | null>(null);
  const busy = useSignal<string | null>(null);
  const copied = useSignal<{ key: string; line: string } | null>(null);
  const problems = useSignal<Record<string, string>>({});
  const timer = useRef<number | undefined>(undefined);
  const hide = () => {
    shown.value = null;
    window.clearTimeout(timer.current);
  };
  // Hidden again when the tab is hidden, the secret changes, or another account is shown.
  useEffect(() => {
    const away = () => { if (document.visibilityState !== "visible") hide(); };
    document.addEventListener("visibilitychange", away);
    return () => { document.removeEventListener("visibilitychange", away); hide(); };
  }, []);
  useEffect(hide, [rows]);
  const setProblem = (slot: string, line: string | null) => {
    const next = { ...problems.value };
    if (line) next[slot] = line; else delete next[slot];
    problems.value = next;
  };
  const flash = (key: string, line: string) => {
    copied.value = { key, line };
    window.setTimeout(() => { if (copied.value?.key === key) copied.value = null; }, copiedSeconds * 1000);
  };
  const request = (name: string, purpose: UserSealedSecretPurpose, part?: UserSealedSecretPart) =>
    ({ name, purpose, ...(part ? { part } : {}), account: auditAccount(account) });
  const reveal = async (slot: string, row: UserSecretRow) => {
    hide();
    busy.value = `${slot}.reveal`;
    try {
      shown.value = { slot, value: await revealSecret(store.link, request(row.name, "reveal")) };
      setProblem(slot, null);
      timer.current = window.setTimeout(hide, shownSeconds * 1000);
    } catch (error) {
      setProblem(slot, revealFailure(error));
    } finally {
      busy.value = null;
    }
  };
  const copySecret = async (slot: string, key: string, row: UserSecretRow, part?: UserSealedSecretPart) => {
    busy.value = key;
    try {
      let value = await revealSecret(store.link, request(row.name, "copy", part));
      if (part) {
        const card = parseCard(value);
        if (!card) { setProblem(slot, revealWords.notCard); return; }
        value = cardParts(card).find((p) => p.part === part)?.value ?? "";
      }
      const line = await copyText(value, true);
      value = "";
      setProblem(slot, null);
      flash(key, line);
    } catch (error) {
      setProblem(slot, error instanceof Error && error.message === revealWords.noClipboard ? revealWords.noClipboard : revealFailure(error));
    } finally {
      busy.value = null;
    }
  };
  const copyPlain = async (key: string, text: string) => {
    try {
      flash(key, await copyText(text, false));
    } catch {
      flash(key, revealWords.noClipboard);
    }
  };
  const copyButton = (key: string, label: string, onClick: () => void) => (
    <button type="button" aria-label={label} disabled={busy.value === key} onClick={onClick}>
      {copied.value?.key === key ? "Copied ✓" : "Copy"}
    </button>
  );
  const plain = (label: string, value: string | undefined) => {
    const text = value?.trim();
    if (!text) return null;
    return (
      <>
        <dt>{label}</dt>
        <dd><span>{text}</span>{copyButton(label, `Copy ${label}`, () => void copyPlain(label, text))}</dd>
      </>
    );
  };
  const slot = (label: string, key: string, n: Named) => {
    if (n.kind === "none") return <><dt>{label}</dt><dd class="hint">{key === "secret" ? `— ${noSecretLine}` : "—"}</dd></>;
    if (n.kind === "elsewhere") return <><dt>{label}</dt><dd class="hint">{n.text} · {notInSecrets}</dd></>;
    const row = n.row;
    const value = shown.value?.slot === key ? shown.value.value : null;
    const card = value === null ? null : parseCard(value);
    return (
      <>
        <dt>{label}</dt>
        <dd>
          <code>{row.name}</code>{value === null && <code class="hint secret-hint">{secretHint(row.hint)}</code>}
          <button type="button" aria-label={`${value === null ? "Reveal" : "Hide"} ${row.name}`} disabled={busy.value === `${key}.reveal`}
            onClick={() => { if (value === null) void reveal(key, row); else hide(); }}>
            {busy.value === `${key}.reveal` ? "…" : value === null ? "Reveal" : "Hide"}
          </button>
          {copyButton(key, `Copy ${row.name}`, () => void copySecret(key, key, row, row.kind === "card" ? "number" : undefined))}
          <button type="button" class="link" aria-label={`${w.openSecret}: ${row.name}`}
            onClick={() => go({ nav: "status", section: "secrets", item: row.name })}>{w.openSecret} →</button>
          {card && cardParts(card).map((p: CardPart) => (
            <span class="account-card-part" key={`${key}.${p.part}`}>
              <span class="hint">{p.label}</span> <code class="no-select">{p.text}</code>
              {copyButton(`${key}.${p.part}`, `Copy ${p.label}`, () => void copySecret(key, `${key}.${p.part}`, row, p.part))}
            </span>
          ))}
          {value !== null && !card && <code class="secret-shown no-select">{value}</code>}
          {value !== null && <span class="hint small">{revealWords.shown}</span>}
          {problems.value[key] && <span class="hint failure" role="status">{problems.value[key]}</span>}
        </dd>
      </>
    );
  };
  const setAsk = async (on: boolean) => {
    problem.value = await save({ ...account, ...(on ? { askBeforeUse: undefined } : { askBeforeUse: false }) });
  };
  const submit = async () => {
    saving.value = true;
    const changed = accountOf(fields.value);
    // Kept by the control plane, not the fields: an edit does not lose them.
    if (account.edited !== undefined) changed.edited = account.edited;
    if (account.lastOp !== undefined) changed.lastOp = account.lastOp;
    problem.value = await save(changed);
    saving.value = false;
    if (!problem.value) editing.value = false;
  };
  const expiry = tokenExpiry(account, Date.now());
  const plan = planLine(account);
  return (
    <article class="secret-detail" aria-label={accountName(account)}>
      <header class="secret-detail-head"><h2>{accountName(account)}</h2></header>
      {editing.value ? (
        <form class="secret-edit" onSubmit={(e) => { e.preventDefault(); void submit(); }}>
          <FieldInputs fields={fields} autofocus />
          <p class="hint">{noSecrets}</p>
          {problem.value && <p class="failure" role="alert">{problem.value}</p>}
          <div class="sheet-actions">
            <button type="button" onClick={() => { editing.value = false; fields.value = accountFields(account); problem.value = null; }}>Cancel</button>
            <button type="submit" class="prominent" disabled={saving.value || !(fields.value.service ?? "").trim()}>Save</button>
          </div>
        </form>
      ) : (
        <>
          <p class="secret-about">{accountAbout(account)}</p>
          <dl class="secret-facts">
            {plain("Service", account.service)}
            {plain("Address", account.address)}
            {plain("User name", account.username)}
            {plan && <><dt>Plan</dt><dd>{plan}</dd></>}
            {slot("Secret", "secret", named(account.secret, rows))}
            {account.card?.trim() && slot("Card", "card", named(account.card, rows))}
            <dt>Two-factor</dt><dd>{account.twoFactor?.trim() || "None"}</dd>
            {expiry && <><dt>Token expires</dt><dd>{expiry}</dd></>}
            <dt>Projects</dt><dd>{account.projects?.length ? account.projects.join(", ") : "All"}</dd>
            <dt>{askBeforeUseWords.toggle}</dt>
            <dd>
              <label class="switch">
                <input type="checkbox" role="switch" checked={accountAsks(account)} disabled={down}
                  aria-label={`${askBeforeUseWords.toggle}: ${accountName(account)}`}
                  onChange={(e) => void setAsk((e.currentTarget as HTMLInputElement).checked)} />
              </label>
              <span class="hint small">{accountAskHint(accountAsks(account))}</span>
            </dd>
            {plain("Notes", account.notes)}
            {account.issue?.trim() && <><dt>Issue</dt><dd>{account.issue}</dd></>}
            {item.needs && <><dt>Needs you</dt><dd class="warning"><Icon name="triangle-alert" />{" "}{item.needs}</dd></>}
          </dl>
          <SecretInfoLine parts={accountLine(account)} class="hint small" />
          <p class="hint" role="status">{copied.value?.line ?? ""}</p>
          {problem.value && <p class="failure" role="alert">{problem.value}</p>}
          <div class="secret-actions">
            <button type="button" disabled={down} onClick={() => { fields.value = accountFields(account); problem.value = null; editing.value = true; }}>{w.edit}</button>
            <span class="spacer" />
            <button type="button" class="destructive" disabled={down} onClick={() => void remove()}>{w.remove}</button>
          </div>
        </>
      )}
    </article>
  );
}
