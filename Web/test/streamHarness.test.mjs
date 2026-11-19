// The web model reaches the daemon's state after drops, duplicates and reconnects (#6187), by the
// recorded streams in Packages/Wire/streams, the same files AgentsKit's StreamHarnessTests play.
import { test } from "node:test";
import assert from "node:assert/strict";
import { load } from "./load.mjs";
import { streams, play, viewOf, daemonAt } from "./streamPlayer.mjs";

const { Work } = await load("test/bounded.ts");

/** A client over the web's Work: the calls its connection makes (store/chatLoading.ts, loadPage). */
function webClient(stream) {
  const host = stream.host;
  const work = new Work();
  work.watch(host, stream.session);
  return {
    notify: (method, params) => work.apply(method, params, host),
    catchUp(snapshot, during) {
      // As loadPage: heard from now is laid after the page; the page from the first entry held
      // resumes it (#6282), and one that leaves a gap opens the chat whole.
      work.heardSincePage = [];
      work.replaceAgents(snapshot.agents, host);
      for (const [method, params] of during) work.apply(method, params, host);
      const start = work.chatResumeStart();
      const page = snapshot.transcript;
      if (start !== null && page.total >= start) {
        const resumed = { ...page, entries: page.entries.slice(start - page.firstIndex), firstIndex: start };
        if (work.takeResumed(resumed)) return;
      }
      work.replaceTranscript(page);
    },
    view: () => ({
      agents: [...(work.agents.value[host] ?? [])].map((a) => ({ id: a.id, title: a.title, state: a.state })).sort((a, b) => a.id.localeCompare(b.id)),
      entries: work.entries.value.map((e) => ({ id: e.id, text: (e.kind.agentMessage ?? e.kind.userMessage)?.text ?? e.kind.userMessage?._0 })),
    }),
  };
}

for (const stream of streams()) {
  for (const scenario of stream.scenarios) {
    // A scenario with a `fault` fails on a bug that is filed: it runs again once the fix removes the field.
    test(`${stream.file}: ${scenario.name}`, { skip: scenario.fault }, () => {
      const client = webClient(stream);
      play(stream, scenario, client);
      assert.deepEqual(client.view(), viewOf(daemonAt(stream, scenario.expect)));
    });
  }
}
