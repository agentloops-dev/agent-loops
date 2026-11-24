// The guided DigitalOcean Connect (#6074; W2 and W2b in epic #6070), in Add a Provider and on
// Reconnect…, as the window's DigitalOceanConnect.swift: Connect DigitalOcean… asks the machine that
// will keep the connection for the consent page (providers/oauthStart), opens it in a new window,
// and gives the hand-back page's code to that machine (providers/oauthFinish). The token never
// reaches this page. Use an API token instead is the fallback. Once connected, the provider's card
// offers New Droplet (#6577).
import { useEffect, useRef, useState } from "preact/hooks";
import type { MachineRef, ProviderOAuthStarted, ProviderSummary } from "../protocol/generated";
import type { Store } from "../model/store";
import { carrierChoices, carrierStart, choiceDocs, kindTitle } from "../model/machineProviders";
import { digitalOceanPreset, doCallback, doWords as words, refusal, type Fix } from "../model/digitalOceanConnect";
import { Icon } from "./Icon";
import { oauthCallbackOrigin } from "../model/connectorSetup";
import { oauthChannel } from "../oauthHandBack";

const signInWindow = "agents-digitalocean";

type Step =
  | { kind: "ready" }
  | { kind: "starting" }
  | { kind: "waiting"; started: ProviderOAuthStarted }
  | { kind: "finishing" }
  | { kind: "refused"; text: string; fix: Fix };

export function DigitalOceanConnect({ store, reconnecting, back, cancel, done }: {
  store: Store; reconnecting?: ProviderSummary | undefined; back?: () => void; cancel: () => void; done: (id: string | undefined) => void;
}) {
  const mac = store.hosts.value.find((h) => store.isMac(h.id))?.id;
  const carriers = carrierChoices(store.hosts.value, store.person.value?.id, mac);
  const [carrier, setCarrier] = useState(carrierStart(reconnecting, carriers));
  const [step, setStep] = useState<Step>({ kind: "ready" });
  const [usesToken, setUsesToken] = useState(false);
  const [token, setToken] = useState("");
  const stopListening = useRef<(() => void) | undefined>(undefined);
  useEffect(() => () => stopListening.current?.(), []);
  const busy = step.kind === "starting" || step.kind === "waiting" || step.kind === "finishing";

  const refuse = (r: { refused: string; reason?: string }) => {
    const said = refusal(r.reason, r.refused);
    setStep({ kind: "refused", text: said.text, fix: said.fix });
  };

  const connect = async () => {
    stopListening.current?.();
    // Opened in the click, so the browser lets it; its address comes once the machine answers.
    const popup = window.open("about:blank", signInWindow);
    setStep({ kind: "starting" });
    const request: Parameters<Store["providers"]["oauthStart"]>[0] = { preset: digitalOceanPreset, origin: location.origin };
    const machine = reconnecting?.machine ?? carrier;
    if (machine) request.machine = machine as MachineRef;
    if (reconnecting) request.id = reconnecting.id;
    const started = await store.providers.oauthStart(request);
    if ("refused" in started) {
      popup?.close();
      refuse(started);
      return;
    }
    let over = false;
    const listen = (event: MessageEvent) => {
      const answer = doCallback(event, started.state);
      if (!answer || over) return;
      over = true;
      stopListening.current?.();
      setStep({ kind: "finishing" });
      void store.providers.oauthFinish(answer).then((ended) => {
        if ("refused" in ended) refuse(ended);
        else done(ended.id);
      });
    };
    window.addEventListener("message", listen);
    // DigitalOcean's pages cut the window off from this one (Cross-Origin-Opener-Policy), so the
    // hand-back page sends the answer to this origin's address, and that tab gives it here.
    const channel = typeof BroadcastChannel === "undefined" ? undefined : new BroadcastChannel(oauthChannel);
    if (channel) channel.onmessage = (event) => listen({ origin: oauthCallbackOrigin, data: event.data } as MessageEvent);
    stopListening.current = () => {
      window.removeEventListener("message", listen);
      channel?.close();
      stopListening.current = undefined;
    };
    setStep({ kind: "waiting", started });
    if (popup && !popup.closed) popup.location.href = started.url;
    else open(started);
  };

  /** Open the Page Again: the same consent page; the sign-in it started still waits. */
  const open = (started: ProviderOAuthStarted) => {
    if (!window.open(started.url, signInWindow)) setStep({ kind: "refused", text: words.popupBlocked, fix: "connect" });
  };

  const stopWaiting = () => {
    stopListening.current?.();
    setStep({ kind: "ready" });
  };

  const addWithToken = async () => {
    const typed = token.trim();
    if (!typed || busy) return;
    stopListening.current?.();
    setStep({ kind: "finishing" });
    const before = new Set((store.providers.list.peek() ?? []).map((p) => p.id));
    const request: Parameters<Store["providers"]["save"]>[0] = {
      name: kindTitle("digitalOcean"), kind: "commands", commands: { preset: digitalOceanPreset }, secrets: { DIGITALOCEAN_API_TOKEN: typed },
    };
    if (carrier) request.machine = carrier as MachineRef;
    const refused = await store.providers.save(request);
    if (refused) setStep({ kind: "refused", text: refused, fix: "connect" });
    else done((store.providers.list.peek() ?? []).find((p) => !before.has(p.id))?.id);
  };

  const connectButton = (
    <p class="do-connect">
      <button type="button" class="prominent" disabled={busy || !carrier} onClick={() => void connect()}>{words.connect}</button>
    </p>
  );
  const keeper = carriers.length === 0 ? <p class="small">{words.noMachine}</p>
    : carriers.length === 1 ? (
      <p class="small">{words.keeps}: <span class="strong">{carriers[0]!.name}</span><br /><span class="quiet">{words.keepsHelp}</span></p>
    ) : (
      <label>{words.keeps}
        <select value={carrier ?? ""} disabled={busy} aria-describedby="do-keeps-help" onChange={(e) => setCarrier((e.target as HTMLSelectElement).value)}>
          {carriers.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
        </select>
        <small class="quiet field-help" id="do-keeps-help">{words.keepsHelp}</small>
      </label>
    );

  return (
    <>
      <div class="provider-head">
        <Icon name="droplet" class="provider-icon" />
        <div class="grow">
          <p class="strong">{reconnecting?.name ?? kindTitle("digitalOcean")}</p>
          <p class="small quiet">{words.intro}</p>
        </div>
        <a class="small" href={choiceDocs("digitalOcean")} target="_blank" rel="noopener noreferrer">{words.howItWorks}</a>
      </div>
      <div class="do-step" role="status" aria-live="polite">
        {(step.kind === "ready" || step.kind === "starting") && (
          <>
            <p>{words.connectLine}</p>
            {connectButton}
            {!reconnecting && (
              <p class="small quiet">{words.noAccount}{" "}
                <a href={words.signUpURL} target="_blank" rel="noopener noreferrer">{words.signUp}</a></p>
            )}
          </>
        )}
        {step.kind === "waiting" && (
          <>
            <p><span class="spinner" aria-hidden="true" /> {words.waiting}</p>
            <p class="small quiet">{words.waitingHelp}</p>
            <p class="do-connect">
              <button type="button" onClick={() => open(step.started)}>{words.openAgain}</button>
              <button type="button" onClick={stopWaiting}>Cancel</button>
            </p>
          </>
        )}
        {step.kind === "finishing" && <p><span class="spinner" aria-hidden="true" /> {words.waiting}</p>}
        {step.kind === "refused" && (
          <>
            <p class="failure">⚠ {step.text}</p>
            {step.fix === "payment" ? (
              <p class="do-connect">
                <a class="button" href={words.billingURL} target="_blank" rel="noopener noreferrer">{words.addPayment}</a>
                <button type="button" onClick={() => void connect()}>{words.checkAgain}</button>
              </p>
            ) : connectButton}
          </>
        )}
      </div>
      {!reconnecting && (
        <>
          <fieldset>{keeper}</fieldset>
          <details class="do-token" open={usesToken} onToggle={(e) => setUsesToken((e.currentTarget as HTMLDetailsElement).open)}>
            <summary>{words.tokenInstead}</summary>
            <label>{words.tokenField}
              <input type="password" value={token} disabled={busy} placeholder="Required" autocomplete="new-password" aria-describedby="do-token-help"
                onInput={(e) => setToken((e.target as HTMLInputElement).value)} />
            </label>
            <small class="quiet field-help" id="do-token-help">{words.tokenScopes}</small>
            <p class="do-connect">
              <a class="small" href={words.tokenPageURL} target="_blank" rel="noopener noreferrer">{words.tokenPage}</a>
              <button type="button" disabled={busy || !carrier || !token.trim()} onClick={() => void addWithToken()}>{words.connectWithToken}</button>
            </p>
          </details>
        </>
      )}
      <div class="sheet-actions">
        {back && <button type="button" class="leading" disabled={busy} onClick={back}>Back</button>}
        <button type="button" onClick={() => { stopListening.current?.(); cancel(); }}>Cancel</button>
      </div>
    </>
  );
}
