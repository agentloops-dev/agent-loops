// Sign In… on a server's Claude or Codex (#3349): the machine has no browser, so the runtime's
// own sign-in runs there and gives a link to open here. Claude's page then gives a code to paste
// back; Codex gives a code to type on its page. An API key is the other way in.
// Not modal (#5203): a panel in the corner, drawn at the page's root from the store, so it stays
// while the person goes to other pages and the page under it works.
import { useEffect, useState } from "preact/hooks";
import type { RuntimeSignInStep } from "../protocol/generated";
import type { RuntimeSignInTarget, Store } from "../model/store";
import { describe } from "../model/errors";
import { machineSignInWords as words } from "../model/runtimes";
import { Icon } from "./Icon";
import { Modal } from "./Modal";

/** How many letters of the link's end always show: its middle is cut when it is long. */
const linkTail = 12;

/** Opens the sign-in panel for a runtime on a machine (#5203). */
export function signInRuntime(store: Store, target: RuntimeSignInTarget): void {
  store.runtimeSignIn.value = target;
}

/** The one sign-in panel, at the page's root; `closed` runs when it goes (the first build reads the runtimes again). */
export function RuntimeSignInPanel({ store, closed }: { store: Store; closed?: (host: string) => void }) {
  const target = store.runtimeSignIn.value;
  if (!target) return null;
  return (
    <RuntimeSignInDialog key={`${target.host}|${target.runtimeID}`} store={store} host={target.host}
      runtimeID={target.runtimeID} runtimeName={target.runtimeName} machine={target.machine}
      close={() => { store.runtimeSignIn.value = null; closed?.(target.host); }} />
  );
}

function RuntimeSignInDialog({ store, host, runtimeID, runtimeName, machine, close }: {
  store: Store; host: string; runtimeID: string; runtimeName: string; machine: string; close: () => void;
}) {
  const [step, setStep] = useState<RuntimeSignInStep>();
  const [byKey, setByKey] = useState(false);
  const [code, setCode] = useState("");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string>();
  const [copied, setCopied] = useState<"link" | "code">();
  const [copies, setCopies] = useState(0);
  const [pasteRefused, setPasteRefused] = useState(false);

  const start = () => {
    setStep(undefined);
    setCode("");
    setPasteRefused(false);
    setProblem(undefined);
    store.signInStart(host, runtimeID).then(setStep, (e: unknown) => setProblem(describe(e)));
  };
  useEffect(() => { if (!byKey) start(); }, [byKey]);

  // A sign-in left waiting is stopped on the machine.
  const leave = () => {
    if (step) void store.signInFinish(host, step.signInID, { cancel: true }).catch(() => {});
    close();
  };
  const run = (work: Promise<void>, again = false) => {
    setBusy(true);
    setProblem(undefined);
    work.then(close, (e: unknown) => {
      setProblem(describe(e));
      // Claude starts again after a code it did not take.
      if (again) setStep(undefined);
    }).finally(() => setBusy(false));
  };
  const finish = () => {
    if (!step) return;
    run(store.signInFinish(host, step.signInID, step.wantsCode ? { code } : {}), step.wantsCode);
  };
  const useKey = () => run(store.signInKey(host, runtimeID, key));
  const switchWay = () => {
    if (step) void store.signInFinish(host, step.signInID, { cancel: true }).catch(() => {});
    setStep(undefined);
    setProblem(undefined);
    setByKey(!byKey);
  };
  const copy = (text: string, what: "link" | "code") => {
    void navigator.clipboard?.writeText(text).then(() => {
      setCopied(what);
      setCopies((n) => n + 1);
    }).catch(() => {});
  };
  // The link's checkmark stays 2 s after the last copy.
  useEffect(() => {
    if (copied !== "link") return undefined;
    const timer = setTimeout(() => setCopied(undefined), 2000);
    return () => clearTimeout(timer);
  }, [copied, copies]);
  // The code cannot be typed, only pasted: with the icon, or with ⌘V when the browser refuses the icon.
  const take = (text: string) => {
    setPasteRefused(false);
    if (text.trim()) setCode(text.trim());
  };
  const paste = async () => {
    try {
      take(await navigator.clipboard.readText());
    } catch {
      setPasteRefused(true);
    }
  };
  const linkLabel = copied === "link" ? words.copied : words.copyLink;
  const title = words.title(runtimeName, machine);
  const ready = byKey ? key.trim() !== "" : !!step && (!step.wantsCode || code.trim() !== "");

  return (
    <Modal label={title} close={leave} modeless>
      <form class="sheet-body connect-sheet runtime-sign-in" onSubmit={(e) => { e.preventDefault(); if (ready && !busy) (byKey ? useKey : finish)(); }}>
        <h2>{title}</h2>
        {byKey ? (
          <div class="token-form">
            <label>{words.keyLabel}
              <input type="password" autocomplete="off" spellcheck={false} value={key}
                onInput={(e) => setKey((e.target as HTMLInputElement).value)} />
            </label>
            <p class="sheet-note">{words.keyNote(runtimeID)}</p>
          </div>
        ) : step ? (
          <>
            <p>{words.openLink}</p>
            <div class="sign-in-field">
              <a href={step.url} target="_blank" rel="noopener noreferrer" title={step.url}><span class="head">{step.url.slice(0, -linkTail)}</span><span>{step.url.slice(-linkTail)}</span></a>
              <button type="button" class="icon" title={linkLabel} aria-label={linkLabel} onClick={() => copy(step.url, "link")}>
                <Icon name={copied === "link" ? "check" : "copy"} />
              </button>
            </div>
            {step.userCode !== undefined ? (
              <>
                <p>{words.typeCode}</p>
                <p class="sign-in-code">
                  <code class="code-text" aria-label={words.codeLabel}>{step.userCode}</code>{" "}
                  <button type="button" onClick={() => copy(step.userCode!, "code")}>{copied === "code" ? words.copied : "Copy"}</button>
                </p>
                <p class="sheet-note">{words.codexNote}</p>
              </>
            ) : step.wantsCode && (
              <div class="token-form">
                <p>{words.pasteCode}</p>
                <div class="sign-in-field">
                  <input aria-label={words.codeLabel} placeholder={words.codeLabel} readOnly autocomplete="off" spellcheck={false} value={code}
                    onPaste={(e) => { e.preventDefault(); take(e.clipboardData?.getData("text/plain") ?? ""); }} />
                  <button type="button" class="icon" title={words.pasteCodeLabel} aria-label={words.pasteCodeLabel} onClick={() => void paste()}>
                    <Icon name="clipboard-paste" />
                  </button>
                </div>
                {pasteRefused && <p class="sheet-note failure" role="alert">{words.pasteRefused}</p>}
              </div>
            )}
          </>
        ) : !problem && <p class="sheet-note" role="status">{words.asking(runtimeName)}</p>}
        {problem && <p class="sheet-note failure" role="alert">{problem}</p>}
        <div class="sheet-actions">
          <button type="button" class="link" onClick={switchWay} disabled={busy}>{byKey ? words.useAccount : words.useKey}</button>
          <button type="button" onClick={leave}>Cancel</button>
          {!byKey && !step && problem
            ? <button type="button" class="prominent" onClick={start}>Try Again</button>
            : <button type="submit" class="prominent" disabled={!ready || busy}>
              {busy ? words.signingIn : byKey || step?.wantsCode ? words.signIn : words.done}
            </button>}
        </div>
      </form>
    </Modal>
  );
}
