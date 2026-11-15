// Add for a Session… (#3548): a secret for one session only, on its machine. The Secrets page's + Add menu (#5257)
// opens it. As the window's SessionSecretSheet.
import { useSignal } from "@preact/signals";
import type { SessionSecrets, UUID } from "../../protocol/generated";
import type { Store } from "../../model/store";
import { describe } from "../../model/errors";
import { describedAs, isSecretName } from "../../model/userScope";
import { sessionSecretWords, sessionsForSecrets } from "../../model/sessionSecrets";
import { AskBeforeUseCheck } from "../AskBeforeUse";
import { Modal } from "../Modal";
import { SecretDescriptionField } from "../SecretDescription";

/** Add for a Session…: which session, a name and a value. The value is never shown again. */
export function SessionSecretSheet({ store, host, offersAsk, close, saved }: {
  store: Store; host: string; offersAsk: boolean; close: () => void; saved: (after: SessionSecrets, agentID: UUID, name: string) => void;
}) {
  const sessions = sessionsForSecrets(store.agents.value[host] ?? []);
  const session = useSignal<UUID | null>(sessions[0]?.id ?? null);
  const name = useSignal("");
  const value = useSignal("");
  // A new secret starts on (#4016, #4162).
  const ask = useSignal(true);
  const description = useSignal("");
  const problem = useSignal<string | null>(null);
  const busy = useSignal(false);
  const chosen = name.value.trim();
  const save = async () => {
    if (!session.value) return;
    busy.value = true;
    try {
      saved(await store.link.call("secrets/sessionSet", { agentID: session.value, name: chosen, value: value.value,
        ...(offersAsk && ask.value ? { needsApproval: true } : {}), ...describedAs(description.value) }, host), session.value, chosen);
      value.value = "";
      close();
    } catch (error) {
      problem.value = describe(error);
    } finally {
      busy.value = false;
    }
  };
  return (
    <Modal label={sessionSecretWords.sheetTitle} close={close}>
      <form method="dialog" class="sheet-body" onSubmit={(e) => { e.preventDefault(); void save(); }}>
        <h2>{sessionSecretWords.sheetTitle}</h2>
        {sessions.length === 0 ? <p class="sheet-note">{sessionSecretWords.noSessions}</p> : (
          <label class="setting">{sessionSecretWords.session}{" "}
            <select value={session.value ?? ""} onChange={(e) => (session.value = e.currentTarget.value as UUID)}>
              {sessions.map((a) => <option key={a.id} value={a.id}>{a.title ?? "Untitled session"}</option>)}
            </select>
          </label>
        )}
        <input type="text" aria-label="Name" placeholder="SHOP_PASSWORD" autofocus autocomplete="off" spellcheck={false}
          value={name.value} onInput={(e) => (name.value = (e.currentTarget as HTMLInputElement).value.toUpperCase())} />
        <input type="password" aria-label="Value" placeholder="Value" autocomplete="off" value={value.value}
          onInput={(e) => (value.value = (e.currentTarget as HTMLInputElement).value)} />
        <SecretDescriptionField text={description} />
        {offersAsk && <AskBeforeUseCheck on={ask} />}
        <p class={problem.value ? "sheet-note failure" : "sheet-note"} role={problem.value ? "alert" : undefined}>
          {problem.value ?? sessionSecretWords.footer}
        </p>
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="submit" class="prominent" disabled={busy.value || !session.value || !value.value || !isSecretName(chosen)}>Add</button>
        </div>
      </form>
    </Modal>
  );
}
