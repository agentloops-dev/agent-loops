// Settings ▸ Machine providers (#1808), the window's Settings ▸ Machine Providers: each
// provider as a card of its workspaces, Add Provider… (a grouped list of kinds, then a form in sections, #6069), Edit…,
// List Again and Remove…, and on a workspace Add as Machine…, Link to Host…, Start, Stop, Stop when
// idle and Unlink.
// Add and Edit… choose the machine that carries it, and its card says which (#3496).
import { Fragment } from "preact";
import { useEffect, useState } from "preact/hooks";
import type { ControlMachine, JSONValue, MachineInfo, MachineSetup as Setup, ProviderID, ProviderSummary } from "../protocol/generated";
import type { Store } from "../model/store";
import {
  addAsMachine, carrierChoices, carrierLine, carrierStart, choiceDescription, choiceDocs, choiceKind, choiceTitle, defaultIdleMinutes, idleChoices, idleRowWords,
  keptAwakeLine, kindChip, kindFields, kindNote, kindOfPreset, kindTitle, linkableHosts, mayBe, movesTo, noHostLine, providerGroups, providerLine,
  providerRequest, providerWords as words, sshDestination, workspaceLine, type ProviderChoice, type ProviderField, type ProviderKind,
} from "../model/machineProviders";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { MachinePicker } from "./MachinePicker";
import { MachineDialog } from "./MachineDialog";
import { makers } from "../model/machineFromRepository";
import { adminsOnly, providerLine as groupLine } from "../model/teamGroups";
import { ask } from "./MessageKinds";
import { hasWarmPools, useWarmPools, WarmPoolsSection } from "./WarmPools";
import { marks, useTick } from "./MachineSetup";
import { offersOneClick, oneClickPlan, oneClickWords, type OneClickPlan } from "../model/oneClickMake";
import { failedStep, isReady, stepLine, stepName, stepStateWords, stepTime } from "../model/machineSetup";
import { describe } from "../model/errors";
import { go } from "../route";
import { DigitalOceanConnect } from "./DigitalOceanConnect";
import { accountLine, doWords, isOAuth } from "../model/digitalOceanConnect";

const time = (at: Date) => at.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });

export function ProvidersSection({ store }: { store: Store }) {
  const providers = store.providers.list.value;
  const problem = store.providers.problem.value;
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<ProviderSummary>();
  const [linking, setLinking] = useState<{ provider: ProviderSummary; workspace: MachineInfo }>();
  const [installing, setInstalling] = useState<{ provider: ProviderSummary; workspace: MachineInfo }>();
  const [said, setSaid] = useState<string | null>(null);
  // Lists every provider again when the page opens (FR-004).
  useEffect(() => { void store.providers.load(); }, []);
  // The warm pools, read again every 30 s while the page is open (#5354).
  useWarmPools(store);
  const off = store.providers.off.value;
  return (
    <section aria-labelledby="settings-providers">
      <h3 class="section-head" id="settings-providers">{words.heading}</h3>
      {off && <p class="hint">{off}</p>}
      {problem && <p class="hint failure">{problem}</p>}
      {said && <p class="hint failure">{said}</p>}
      {!off && providers?.map((p) => (
        <Fragment key={p.id}>
          <ProviderCard store={store} provider={p} say={setSaid} edit={() => setEditing(p)}
            link={(workspace) => setLinking({ provider: p, workspace })} install={(workspace) => setInstalling({ provider: p, workspace })} />
          {/* A provider that makes machines can keep some ready (#5354). */}
          {hasWarmPools(store, p) && <WarmPoolsSection store={store} provider={p} />}
        </Fragment>
      ))}
      {!off && (
        <>
          {/* Only an admin adds one (#1761). */}
          {store.person.value?.role !== "member" && (
            <div class="provider-add"><button type="button" onClick={() => setAdding(true)}>{words.add}</button></div>
          )}
          <p class="hint">{words.footer(store.hasMac.value)}</p>
        </>
      )}
      {(adding || editing) && (
        <AddProviderDialog store={store} editing={editing} close={() => { setAdding(false); setEditing(undefined); }} />
      )}
      {linking && (
        <LinkDialog store={store} provider={linking.provider} workspace={linking.workspace} close={() => setLinking(undefined)} />
      )}
      {installing && (
        <InstallDialog store={store} provider={installing.provider} workspace={installing.workspace} close={() => setInstalling(undefined)} />
      )}
    </section>
  );
}

function ProviderCard({ store, provider, say, edit, link, install }: {
  store: Store; provider: ProviderSummary; say: (why: string | null) => void; edit: () => void; link: (w: MachineInfo) => void;
  install: (w: MachineInfo) => void;
}) {
  const hosts = store.hosts.value;
  const groups = store.providers.groups.value;
  const admin = store.person.value?.role !== "member";
  // W6 (#6074): an OAuth connection says whose account, and when DigitalOcean stopped taking it.
  const oauth = isOAuth(provider) ? provider.oauth : undefined;
  const line = [oauth?.state === "disconnected" ? doWords.disconnected : undefined, oauth ? accountLine(oauth) : undefined,
    oauth?.state === "disconnected" ? undefined : providerLine(provider, time), groupLine(provider, groups),
    carrierLine(provider, admin && !!kindOfPreset(provider.preset))].filter(Boolean).join(" · ");
  const [making, setMaking] = useState(false);
  const [reconnecting, setReconnecting] = useState(false);
  const setGroup = async (group: string) => say(await store.providers.setGroup(provider.id, group || null));
  const remove = async () => {
    if (!await ask(`${words.removeTitle(provider.name)} ${oauth ? doWords.removeNote : words.removeNote}`)) return;
    say(await store.providers.remove(provider.id));
  };
  return (
    <ul class="setup-card list provider-card" aria-label={provider.name}>
      <li class="connection">
        <div class="grow">
          <p class="strong">{provider.name} <span class="chip">{oauth ? (oauth.state === "disconnected" ? doWords.disconnectedChip : doWords.connectedChip) : kindChip(provider)}</span>
            {provider.default && <span class="chip default-chip" title="New Machine makes your machines here">Default</span>}</p>
          {line && <p class="small quiet">{line}</p>}
        </div>
        {/* The same New Machine as Add a Machine…, at this provider (#2131). A DigitalOcean card has New Droplet and
            Change… below in its place (#6577). */}
        {makers([provider]).length > 0 && !offersOneClick(provider) && (
          <button type="button" onClick={() => setMaking(true)}>New Machine…</button>
        )}
        {/* The provider New Machine uses (#3979). */}
        {makers([provider]).length > 0 && !provider.default && (
          <button type="button" class="link small" onClick={() => void store.providers.makeDefault(provider.id).then(say)}>Make Default</button>
        )}
        <button type="button" class="link small" onClick={() => void store.providers.refresh(provider.id)}>List Again</button>
        {/* Who may start machines from it (#1761): an admin gives it to a group. */}
        {admin && (
          <select aria-label={`${provider.name}’s group`} value={provider.group ?? ""}
            onChange={(e) => void setGroup((e.currentTarget as HTMLSelectElement).value)}>
            <option value="">{adminsOnly}</option>
            {groups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </select>
        )}
        {admin && oauth && <button type="button" onClick={() => setReconnecting(true)}>{doWords.reconnect}</button>}
        {admin && kindOfPreset(provider.preset) && <button type="button" onClick={edit}>Edit…</button>}
        {admin && <button type="button" class="destructive" onClick={() => void remove()}>Remove…</button>}
      </li>
      {offersOneClick(provider) && <NewDropletRow store={store} provider={provider} change={() => setMaking(true)} />}
      {provider.workspaces.map((w) => (
        <WorkspaceRow key={w.id} store={store} provider={provider} workspace={w} hosts={hosts} say={say} link={() => link(w)} install={() => install(w)} />
      ))}
      {making && <MachineDialog store={store} machine picked={provider.id} close={() => setMaking(false)} />}
      {reconnecting && (
        <Modal label={`${doWords.reconnect.replace("…", "")} ${provider.name}`} close={() => setReconnecting(false)}>
          <div class="sheet-body connect-sheet provider-sheet">
            <DigitalOceanConnect store={store} reconnecting={provider} cancel={() => setReconnecting(false)} done={() => setReconnecting(false)} />
          </div>
        </Modal>
      )}
    </ul>
  );
}

function WorkspaceRow({ store, provider, workspace, hosts, say, link, install }: {
  store: Store; provider: ProviderSummary; workspace: MachineInfo; hosts: readonly ControlMachine[];
  say: (why: string | null) => void; link: () => void; install: () => void;
}) {
  const host = workspace.host ? hosts.find((h) => h.id === workspace.host) : undefined;
  const linked = host?.workspace;
  const starting = store.hostsBeingStarted.value.includes(host?.id ?? "");
  const stop = async () => {
    if (!host) return;
    let answer = await store.providers.stop(host.id);
    if (answer && typeof answer === "object") {
      if (!await ask(answer.question)) return;
      answer = await store.providers.stop(host.id, true);
    }
    say(typeof answer === "string" ? answer : null);
  };
  const unlink = async () => {
    if (!host || !await ask(`Unlink ${host.name} from ${workspace.name}? ${provider.name} can no longer start or stop it.`)) return;
    say(await store.providers.unlink(host.id));
  };
  const setIdle = async (minutes: number) => { if (host) say(await store.providers.setIdle(host.id, minutes)); };
  // A make that failed is no machine (#6078): its row offers only Delete.
  const failed = workspace.state === "failed";
  const deleteFailed = async () => {
    if (!await ask(words.deleteFailed(workspace.name, provider.name))) return;
    say(await store.providers.deleteFailed(provider.id, workspace.id));
  };
  // Made here and joining by itself (#6577): its steps show below, and nothing is to be linked or added yet.
  const joining = !workspace.host && workspace.joining === true;
  const free = failed || joining ? [] : linkableHosts(workspace, hosts);
  const noHost = noHostLine(workspace, hosts);
  const adding = joining ? undefined : addAsMachine(workspace);
  const awake = linked ? keptAwakeLine(linked) : null;
  return (
    <>
    <li class="connection workspace-row">
      <span class={`dot ${workspace.state === "running" ? "online" : workspace.state === "unknown" ? "attention" : "offline"}`} aria-hidden="true" />
      <div class="grow">
        <p>{workspace.name}</p>
        <p class="small quiet">{linked?.phase ?? workspaceLine(workspace, hosts)}</p>
        {noHost && <p class="small quiet">{noHost}</p>}
        {awake && <p class="small quiet">{awake}</p>}
      </div>
      {host && linked?.canStart && (
        <button type="button" disabled={starting} onClick={() => void store.startHost(host.id)}>{starting ? "Starting…" : "Start"}</button>
      )}
      {host && linked?.canStop && <button type="button" onClick={() => void stop()}>Stop</button>}
      {host && linked && (
        <select aria-label={`${host.name}: Stop when idle`} title="Stop when idle" value={String(linked.stopWhenIdleMinutes ?? 0)}
          onChange={(e) => void setIdle(Number((e.currentTarget as HTMLSelectElement).value))}>
          {idleChoices.map((c) => <option key={c.minutes} value={String(c.minutes)}>{idleRowWords(c.minutes)}</option>)}
        </select>
      )}
      {host && <button type="button" class="link small" onClick={() => void unlink()}>Unlink</button>}
      {!workspace.host && free.length > 0 && (
        <button type="button" onClick={link}>
          {free.length === 1 && mayBe(workspace, free[0]!.name) ? `Link to ${free[0]!.name}…` : "Link to Machine…"}
        </button>
      )}
      {adding && (
        <button type="button" disabled={!adding.ready} title={adding.help} onClick={install}>{words.addAsMachine}</button>
      )}
      {failed && <button type="button" onClick={() => void deleteFailed()}>Delete…</button>}
      {joining && <span class="small quiet">Joining…</span>}
    </li>
    {/* A make's steps on its row, from the press until it joins (#6577, W4). */}
    {!workspace.host && workspace.setup && <li class="provider-setup"><ProviderSetupSteps setup={workspace.setup} /></li>}
    </>
  );
}

/** New Droplet on a connected DigitalOcean provider's card (#6577, epic #6070 W3), as the window's NewDropletRow: one
 * click makes a machine with what New Machine would make, and the line beside it says where and what it costs. Change…
 * opens the whole New Machine form. The make's steps show on its row (W4), and Open Machine once it joins (W5). */
function NewDropletRow({ store, provider, change }: { store: Store; provider: ProviderSummary; change: () => void }) {
  const [plan, setPlan] = useState<OneClickPlan | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [working, setWorking] = useState(false);
  const [made, setMade] = useState<string[]>([]);
  // Read again when the provider's row changes: a make takes the next name.
  const count = provider.workspaces.length;
  useEffect(() => {
    let live = true;
    void store.providers.options(provider.id)
      .then((o) => {
        if (!live) return;
        const p = oneClickPlan(o);
        setPlan(p);
        if (!p) setProblem(`${provider.name} gives no name for a new machine: use ${oneClickWords.change}`);
      })
      .catch((error: unknown) => { if (live) { setPlan(null); setProblem(describe(error)); } });
    return () => { live = false; };
  }, [provider.id, count]);
  const drop = (name: string) => setMade((m) => m.filter((n) => n !== name));
  const press = async () => {
    if (!plan) return;
    setWorking(true);
    setProblem(null);
    setMade((m) => [...m, plan.name]);
    try {
      // The call answers once DigitalOcean made it; its row shows each step meanwhile.
      const answer = await store.link.call("providers/create",
        { id: provider.id as ProviderID, name: plan.name, options: plan.values as Record<string, JSONValue> });
      if (answer.workspace.state === "failed") drop(plan.name);
    } catch (error) {
      drop(plan.name);
      setProblem(describe(error));
    } finally {
      setWorking(false);
      void store.providers.refresh(provider.id);
    }
  };
  const ready = store.hosts.value.filter((h) => made.includes(h.name) && (!h.setup || isReady(h.setup)));
  const usable = provider.mayUse !== false;
  return (
    <li class="connection new-droplet">
      <div class="grow">
        <div class="row-actions">
          <button type="button" class="prominent" disabled={!plan || working || !usable} title={plan ? oneClickWords.note(plan.name) : undefined}
            onClick={() => void press()}>{working ? "Making…" : oneClickWords.button}</button>
          <button type="button" disabled={working || !usable} onClick={change}>{oneClickWords.change}</button>
          {plan && <span class="small quiet">{plan.summary}</span>}
        </div>
        {plan && <p class="small quiet">{oneClickWords.note(plan.name)}</p>}
        {problem && <p class="small failure" role="alert">{problem}</p>}
        {ready.map((h) => (
          <div key={h.id} class="row-actions new-droplet-ready">
            <Icon name="circle-check" title="Ready" />
            <span>{oneClickWords.ready(h.name)}</span>
            <button type="button" onClick={() => drop(h.name)}>Done</button>
            <button type="button" class="prominent" onClick={() => { drop(h.name); go({ nav: "machines", section: `machine-${h.id}` }); }}>
              {oneClickWords.openMachine}</button>
          </div>
        ))}
      </div>
    </li>
  );
}

/** A make's steps on its provider's row (#6577, W4), as the window's ProviderSetupSteps: a mark, the step and its line,
 * and the time a running step has taken. The machine's page has the whole set-up, with Try Again, once it joins. */
function ProviderSetupSteps({ setup }: { setup: Setup }) {
  const failed = failedStep(setup);
  const now = useTick(!isReady(setup) && !failed);
  return (
    <>
      <ol class="setup-steps">
        {setup.steps.map((step) => (
          <li key={step.id} class={`setup-step ${step.state}`}>
            <span class="setup-mark"><Icon name={marks[step.state]} title={stepStateWords[step.state]} /></span>
            <span class="setup-words">
              <span>{stepName(step)}</span>
              <span class="small quiet setup-line">{stepLine(step, now)}</span>
            </span>
            <span class="small quiet setup-time">{stepTime(step, now)}</span>
          </li>
        ))}
      </ol>
      {failed && <p class="small failure" role="alert">{failed.error ?? `${stepName(failed)} failed.`}</p>}
    </>
  );
}

/** Each row's icon, as the window's Kind.symbol (design/icons.json). */
const choiceIcons: Record<ProviderChoice, IconName> = {
  digitalOcean: "droplet", compute: "cpu", aws: "server", codespaces: "code", coder: "square-terminal",
  workstations: "cloud", docker: "package", commands: "square-terminal",
};

/**
 * Add a Provider, or Edit… one (#6069): step 1, a grouped list of the kinds with a line each;
 * step 2, a short form in sections, with help under each field, and one foot. With `added` (Add a
 * Machine, #2131) it goes on with the provider it added, in place of closing.
 */
export function AddProviderDialog({ store, editing, close, added }: {
  store: Store; editing?: ProviderSummary | undefined; close: () => void; added?: (id: string | undefined) => void;
}) {
  const [kind, setKind] = useState<ProviderKind | undefined>(kindOfPreset(editing?.preset));
  const [name, setName] = useState(editing?.name ?? "");
  // The values, secrets among them, live in this sheet's state only, and go when it closes.
  const [values, setValues] = useState<Record<string, string>>({});
  const [problem, setProblem] = useState<string>();
  const [working, setWorking] = useState(false);
  // The machine that carries it (#3496): the person's online ones, their Mac first.
  const mac = store.hosts.value.find((h) => store.isMac(h.id))?.id;
  const carriers = carrierChoices(store.hosts.value, store.person.value?.id, mac);
  const [carrier, setCarrier] = useState(carrierStart(editing, carriers));
  const moving = !!editing && movesTo(editing, carrier);
  const offlineCarrier = editing?.machine && !carriers.some((h) => h.id === editing.machine) ? editing : undefined;
  const fields = kind ? kindFields(kind).filter((f) => !editing || f.secret) : [];
  const shown = (id: string) => values[id] ?? fields.find((f) => f.id === id)?.value ?? "";
  const choose = (k: ProviderKind) => {
    setKind(k);
    setValues(Object.fromEntries(kindFields(k).filter((f) => f.value).map((f) => [f.id, f.value!])));
    setProblem(undefined);
  };
  const back = () => { setKind(undefined); setProblem(undefined); };
  const save = async () => {
    if (!kind) return;
    const request = providerRequest(kind, name, values, editing, carrier);
    if (typeof request === "string") return setProblem(request);
    setWorking(true);
    setProblem(undefined);
    const before = new Set((store.providers.list.peek() ?? []).map((p) => p.id));
    const refused = await store.providers.save(request);
    setWorking(false);
    if (refused) setProblem(refused);
    else if (added) added((store.providers.list.peek() ?? []).find((p) => !before.has(p.id))?.id);
    else close();
  };
  const title = editing ? `Edit ${editing.name}` : kind ? `Add ${kindTitle(kind)}` : "Add a Provider";
  const input = (f: ProviderField) => {
    const help = f.help ? `provider-help-${f.id}` : undefined;
    const set = (value: string) => setValues({ ...values, [f.id]: value });
    const control = f.id === "serviceKey"
      ? <textarea rows={4} value={shown(f.id)} disabled={working} autocomplete="off" spellcheck={false} aria-describedby={help}
          placeholder={moving ? words.giveAgain : editing ? words.editSecret : undefined}
          onInput={(e) => set((e.target as HTMLTextAreaElement).value)} />
      : f.secret
      ? <input type="password" value={shown(f.id)} disabled={working} aria-describedby={help}
          placeholder={moving ? words.giveAgain : editing ? words.editSecret : f.prompt}
          autocomplete="new-password" onInput={(e) => set((e.target as HTMLInputElement).value)} />
      : <input type="text" value={shown(f.id)} disabled={working} placeholder={f.prompt} autocomplete="off" aria-describedby={help}
          autocapitalize="off" spellcheck={false} onInput={(e) => set((e.target as HTMLInputElement).value)} />;
    return (
      <Fragment key={f.id}>
        <label>{f.label}{f.optional ? " (optional)" : ""}
          {control}
        </label>
        {f.help && <small class="quiet field-help" id={help}>{f.help}</small>}
      </Fragment>
    );
  };
  const guided = kind === "digitalOcean" && !editing;
  const access = fields.filter((f) => f.secret);
  const machines = fields.filter((f) => !f.secret);
  return (
    <Modal label={title} close={close}>
      <div class="sheet-body connect-sheet provider-sheet">
        <p class="strong">{title}</p>
        {guided ? (
          // Guided (#6074): Connect with OAuth, or a token behind a disclosure.
          <DigitalOceanConnect store={store} back={back} cancel={close} done={(id) => (added ? added(id) : close())} />
        ) : !kind ? (
          <>
            <p class="small quiet">{words.chooseIntro}</p>
            {providerGroups.map((group) => (
              <section key={group.title} class="provider-group" aria-label={group.title}>
                <h4 class="section-head">{group.title}</h4>
                <ul class="provider-choices">
                  {group.kinds.map((choice) => {
                    const opens = choiceKind(choice);
                    return (
                      <li key={choice}>
                        <button type="button" class="provider-choice" disabled={!opens}
                          onClick={() => { if (opens) choose(opens); }}>
                          <Icon name={choiceIcons[choice]} class="provider-icon" />
                          <span class="grow">
                            <span class="strong">{choiceTitle(choice)}</span>
                            <span class="small quiet">{choiceDescription(choice)}</span>
                          </span>
                          {/* W1 (#6074): the guided kind says so. */}
                          {choice === "digitalOcean" && <span class="chip">{doWords.guidedChip}</span>}
                          {opens ? <Icon name="chevron-right" class="quiet" /> : <span class="chip">{words.inTheMacApp(store.hasMac.value)}</span>}
                        </button>
                      </li>
                    );
                  })}
                </ul>
              </section>
            ))}
          </>
        ) : (
          <form class="token-form provider-form" onSubmit={(e) => { e.preventDefault(); void save(); }}>
            <div class="provider-head">
              <Icon name={choiceIcons[kind]} class="provider-icon" />
              <div class="grow">
                <p class="strong">{kindTitle(kind)}</p>
                <p class="small quiet">{choiceDescription(kind)}{" "}{kindNote(kind)}</p>
              </div>
              <a class="small" href={choiceDocs(kind)} target="_blank" rel="noopener noreferrer">{words.howToSetUp}</a>
            </div>
            <fieldset>
              <legend>Provider</legend>
              <label>Name
                <input type="text" value={name} placeholder={kindTitle(kind)} disabled={working} autocomplete="off"
                  onInput={(e) => setName((e.target as HTMLInputElement).value)} />
              </label>
              {carriers.length === 0 && !offlineCarrier ? <p class="small">{words.noneOnline}</p> : (
                <label>{words.carriedBy}
                  <select value={carrier ?? ""} disabled={working} onChange={(e) => setCarrier((e.target as HTMLSelectElement).value)}>
                    {offlineCarrier && <option value={offlineCarrier.machine}>{offlineCarrier.machineName ?? "Its machine"} (offline)</option>}
                    {carriers.map((h) => <option key={h.id} value={h.id}>{h.name}</option>)}
                  </select>
                </label>
              )}
              {moving && <small class="quiet">{words.movedNote}</small>}
            </fieldset>
            {access.length > 0 && (
              <fieldset>
                <legend>Access</legend>
                {access.map(input)}
              </fieldset>
            )}
            {machines.length > 0 && (
              <fieldset>
                <legend>Machines</legend>
                {machines.map(input)}
              </fieldset>
            )}
            {/* Return in a field adds it, as the default button. */}
            <button type="submit" hidden />
          </form>
        )}
        {problem && <p class="small failure">{problem}</p>}
        {kind && !guided && <p class="small quiet provider-foot">{words.secretsFoot(carriers.find((h) => h.id === carrier)?.name ?? editing?.machineName)}</p>}
        {!guided && <div class="sheet-actions">
          {kind && !editing && <button type="button" class="leading" disabled={working} onClick={back}>Back</button>}
          <button type="button" onClick={close}>Cancel</button>
          {kind && (
            <button type="button" class="prominent" disabled={working || !carrier} onClick={() => void save()}>
              {working ? <><span class="spinner" aria-hidden="true" /> {words.checking}</> : editing ? "Save" : "Add"}
            </button>
          )}
        </div>}
      </div>
    </Modal>
  );
}

/** Link to Machine (#691 L5): a host that already runs on this workspace, with when to start and stop it. */
function LinkDialog({ store, provider, workspace, close }: {
  store: Store; provider: ProviderSummary; workspace: MachineInfo; close: () => void;
}) {
  const hosts = linkableHosts(workspace, store.hosts.value);
  const [host, setHost] = useState(hosts[0]?.id ?? "");
  const [startWhenUsed, setStartWhenUsed] = useState(true);
  const [idle, setIdle] = useState(defaultIdleMinutes);
  const [problem, setProblem] = useState<string | null>(null);
  const [fingerprint, setFingerprint] = useState<string>();
  const [working, setWorking] = useState(false);
  const link = async () => {
    setWorking(true);
    setProblem(null);
    const outcome = await store.providers.link(host, provider.id, workspace.id, startWhenUsed, idle, fingerprint);
    setWorking(false);
    // A machine that joined with the command: its host key, to trust first (#3379).
    if ("needsTrust" in outcome) return setFingerprint(outcome.needsTrust);
    setFingerprint(undefined);
    if ("failed" in outcome) setProblem(outcome.failed);
    else close();
  };
  return (
    <Modal label={`Link ${workspace.name} to a Machine`} close={close}>
      <div class="sheet-body connect-sheet provider-sheet">
        <p class="strong">Link {workspace.name} to a Machine</p>
        <p class="small quiet">Agent Loops already runs on this machine: {provider.name} starts and stops it, and nothing is installed again.</p>
        {hosts.length === 0 ? <p class="small">{words.noHosts}</p> : (
          <form class="token-form" onSubmit={(e) => { e.preventDefault(); void link(); }}>
            <label>Machine
              <MachinePicker store={store} label="Machine" value={host || null} disabled={working || !!fingerprint}
                skip={(h) => !hosts.some((o) => o.id === h.id)} onPick={(id) => setHost(id ?? "")} />
            </label>
            <label class="setting">
              <span>Start when used</span>
              <input type="checkbox" role="switch" checked={startWhenUsed} onChange={(e) => setStartWhenUsed(e.currentTarget.checked)} />
            </label>
            <label>Stop when idle
              <select value={String(idle)} onChange={(e) => setIdle(Number((e.target as HTMLSelectElement).value))}>
                {idleChoices.map((c) => <option key={c.minutes} value={String(c.minutes)}>{c.label}</option>)}
              </select>
            </label>
          </form>
        )}
        {fingerprint && <p class="small">{words.linkTrustNote(fingerprint)}</p>}
        {working && <p class="quiet small">{words.linking}</p>}
        {problem && <p class="small failure">{problem}</p>}
        <div class="sheet-actions">
          <button type="button" onClick={close}>Cancel</button>
          <button type="button" class="prominent" disabled={!host || working} onClick={() => void link()}>
            {fingerprint ? "Trust and Link" : "Link"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * Add as Machine… (#3365), the window's ControlAddServerSheet as a workspace fills it in: the
 * control plane installs Agents over its ssh (gh's, for a codespace), a new host key is shown
 * to trust first, and the machine is linked to the workspace once it joins.
 */
function InstallDialog({ store, provider, workspace, close }: {
  store: Store; provider: ProviderSummary; workspace: MachineInfo; close: () => void;
}) {
  const [name, setName] = useState(workspace.name);
  const [destination, setDestination] = useState(sshDestination(workspace.ssh ?? ""));
  const [fingerprint, setFingerprint] = useState<string>();
  const [working, setWorking] = useState(false);
  const [problem, setProblem] = useState<string>();
  const [installed, setInstalled] = useState<string>();
  const install = async () => {
    if (!destination.trim()) return setProblem("Give the ssh destination.");
    setWorking(true);
    setProblem(undefined);
    const outcome = await store.providers.install(destination.trim(), name.trim(), provider.id, workspace.id, fingerprint);
    setWorking(false);
    if ("needsTrust" in outcome) return setFingerprint(outcome.needsTrust);
    setFingerprint(undefined);
    if ("failed" in outcome) return setProblem(outcome.failed);
    setInstalled(outcome.added);
    // It may have joined while the install ran.
    store.providers.linkJoined(store.hosts.peek());
  };
  return (
    <Modal label={`Add ${workspace.name} as a Machine`} close={close}>
      <div class="sheet-body connect-sheet provider-sheet">
        <p class="strong">Add {workspace.name} as a Machine</p>
        <p class="small quiet">{words.installNote(provider.name)}</p>
        {installed ? <p class="small">{words.installed(installed)}</p> : (
          <form class="token-form" onSubmit={(e) => { e.preventDefault(); void install(); }}>
            <label>Name
              <input type="text" value={name} disabled={working} autocomplete="off" spellcheck={false}
                onInput={(e) => setName((e.target as HTMLInputElement).value)} />
            </label>
            <label>ssh destination
              <input type="text" value={destination} disabled={working || !!fingerprint} autocomplete="off" autocapitalize="off" spellcheck={false}
                onInput={(e) => setDestination((e.target as HTMLInputElement).value)} />
            </label>
          </form>
        )}
        {fingerprint && <p class="small">{words.trustNote(fingerprint)}</p>}
        {working && <p class="quiet small">{words.installing}</p>}
        {problem && <p class="small failure">{problem}</p>}
        <div class="sheet-actions">
          {installed ? <button type="button" class="prominent" onClick={close}>Done</button> : (
            <>
              <button type="button" onClick={close}>Cancel</button>
              <button type="button" class="prominent" disabled={working} onClick={() => void install()}>
                {fingerprint ? "Trust and Install" : "Install"}
              </button>
            </>
          )}
        </div>
      </div>
    </Modal>
  );
}
