// Settings ▸ Control plane ▸ Clients (#4253), as the window's: Pair a Client… (#7270) opens one dialog with the
// kind of client to choose: an iPhone or iPad scans a QR code, a Mac and a browser paste a code. Each code is made
// when the dialog opens or the kind changes, and works once; the dialog names the client it let in. A Mac's code also shows as loops auth login takes it, and Command line the
// lines that put loops on the PATH, each to copy (#6875). Listed in ./sections.ts.
import { useEffect, useState } from "preact/hooks";
import { useSignal } from "@preact/signals";
import type { ClientRecord, ControlCodeShown } from "../../protocol/generated";
import { fromWireDate } from "../../protocol/dates";
import { describe } from "../../model/errors";
import { codeInstructions, commandLine, type PairPurpose, pairPurposes, pairWords as words, qrModules, remainingLine, terminalLogin } from "../../model/pairClients";
import type { Store } from "../../model/store";
import { Modal } from "../Modal";

export function ClientsSection({ store, down }: { store: Store; down: boolean }) {
  const [pairing, setPairing] = useState(false);
  return (
    <section aria-labelledby="settings-clients">
      <h3 class="section-head" id="settings-clients">{words.heading}</h3>
      <div class="pair-buttons">
        <button type="button" disabled={down} onClick={() => setPairing(true)}>{words.button}</button>
      </div>
      <p class="hint">{words.hint}</p>
      <CommandLine />
      {pairing && <PairDialog store={store} close={() => setPairing(false)} />}
    </section>
  );
}

/** The lines that put loops on the PATH (#6875), each with Copy. */
function CommandLine() {
  const copied = useSignal<string | null>(null);
  const copy = (line: string) => {
    void navigator.clipboard?.writeText(line).then(() => { copied.value = line; }).catch(() => {});
  };
  return (
    <div class="join-command" role="group" aria-labelledby="settings-command-line">
      <h4 class="section-head" id="settings-command-line">{commandLine.heading}</h4>
      <p class="hint">{commandLine.hint}</p>
      {commandLine.lines.map(({ label, line }) => (
        <div key={line}>
          <p class="hint">{label}</p>
          <div class="join-command-line">
            <code class="code-text">{line}</code>
            <button type="button" onClick={() => copy(line)}>{copied.value === line ? "Copied" : "Copy"}</button>
          </div>
        </div>
      ))}
      <p class="hint">{commandLine.releases}</p>
    </div>
  );
}

function PairDialog({ store, close }: { store: Store; close: () => void }) {
  const [purpose, setPurpose] = useState<PairPurpose>("device");
  const shown = useSignal<ControlCodeShown | null>(null);
  const problem = useSignal<string | null>(null);
  const copied = useSignal(false);
  const copiedLogin = useSignal(false);
  const before = useSignal<Set<string> | null>(null);
  const joined = useSignal<ClientRecord | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => {
      clearInterval(tick);
      void store.link.call("clients/stopPairing", {}).catch(() => {});
    };
  }, []);
  // A change of kind makes a new code: the old one stops first, so it can't stop the new one.
  useEffect(() => {
    let live = true;
    shown.value = null;
    problem.value = null;
    joined.value = null;
    before.value = null;
    copied.value = false;
    copiedLogin.value = false;
    store.link.call("clients/stopPairing", {}).catch(() => {})
      // Who was there first, so whoever the code lets in can be named.
      .then(() => store.link.call("clients/list", {}))
      .then((clients) => { if (live) before.value = new Set(clients.map((c) => c.id)); })
      .then(() => (live ? store.link.call("clients/startPairing", purpose === "browser" ? { kind: "browser" } : {}) : null))
      .then((code) => { if (live && code) shown.value = code; })
      .catch((error) => { if (live) problem.value = describe(error); });
    return () => { live = false; };
  }, [purpose]);
  const changes = store.clientChanges.value;
  useEffect(() => {
    const was = before.value;
    if (!was || !shown.value || joined.value) return;
    store.link.call("clients/list", {})
      .then((clients) => { joined.value = clients.find((c) => !was.has(c.id) && !c.forgotten) ?? null; })
      .catch(() => {});
  }, [changes]);
  const code = shown.value;
  const expires = code ? fromWireDate(code.expires).getTime() : 0;
  const copy = () => {
    if (code) void navigator.clipboard?.writeText(code.text).then(() => { copied.value = true; }).catch(() => {});
  };
  // A Mac's code pairs a terminal too, as the command that takes it (#6875).
  const copyLogin = () => {
    if (code) void navigator.clipboard?.writeText(terminalLogin(code.text)).then(() => { copiedLogin.value = true; }).catch(() => {});
  };
  const title = words.title;
  const plane = store.plane.value;
  return (
    <Modal label={title} close={close}>
      <div class="sheet-body invite-code">
        <h2>{title}</h2>
        <div class="button-group pair-kinds" role="radiogroup" aria-label={words.kindLabel}>
          {pairPurposes.map((p) => (
            <button key={p} type="button" role="radio" aria-checked={purpose === p} class={purpose === p ? "on" : ""}
              onClick={() => setPurpose(p)}>{words.kind(p)}</button>
          ))}
        </div>
        {joined.value ? (
          purpose === "device" ? (
            <>
              <p class="sheet-note" role="status">{words.paired(joined.value.name)}</p>
              <p class="sheet-note">{words.pairedNote}</p>
            </>
          ) : <p class="sheet-note" role="status">{words.joined(joined.value.name)}</p>
        ) : code ? (
          purpose === "device" ? (
            <>
              <p class="sheet-note">{words.scan}</p>
              <QRCode text={code.text} />
              <p class="sheet-note quiet" aria-live="off">{remainingLine(expires, now)}</p>
            </>
          ) : (
            <>
              <code class="code-text" aria-label="Code">{code.text}</code>
              <p class="sheet-note">{codeInstructions(purpose, expires, now, plane?.web, plane?.publicWeb)}</p>
              {purpose === "mac" && (
                <>
                  <p class="sheet-note">{words.terminal}</p>
                  <code class="code-text" aria-label="Command">{terminalLogin(code.text)}</code>
                </>
              )}
            </>
          )
        ) : !problem.value && <p class="sheet-note" role="status">{words.making}</p>}
        {problem.value && <p class="sheet-note failure" role="alert">{problem.value}</p>}
        <div class="sheet-actions">
          {code && !joined.value && purpose === "mac" && (
            <button type="button" onClick={copyLogin}>{copiedLogin.value ? "Copied" : "Copy Command"}</button>
          )}
          {code && !joined.value && purpose !== "device" && (
            <button type="button" onClick={copy}>{copied.value ? "Copied" : "Copy"}</button>
          )}
          <button type="button" class="prominent" onClick={close}>{joined.value || purpose !== "device" ? "Done" : "Cancel"}</button>
        </div>
      </div>
    </Modal>
  );
}

/** A QR code, dark on light whatever the theme, sharp at any size: one square per module. */
function QRCode({ text }: { text: string }) {
  const { size, dark } = qrModules(text);
  return (
    <svg class="pair-qr" viewBox={`0 0 ${size} ${size}`} role="img" aria-label="Pairing code" shape-rendering="crispEdges">
      <rect width={size} height={size} fill="white" />
      <path fill="black" d={dark.map(([x, y]) => `M${x} ${y}h1v1h-1z`).join("")} />
    </svg>
  );
}
