// A secret an agent saved on one of the person's machines (#5308, specs/5308-agent-secrets-sync), as the
// window's and the Remote's card: at the top of the sidebar, beside the machines that ask to join. A name the
// person has already waits for Keep Yours or Use the Agent's; any other is taken by this page at once, so it
// shows here only until then. A card is keyed by its machine and name, not the offer's id (#6626): a new offer of
// the same name, read while Use the Agent's runs, keeps its progress and its failure.
import { useSignal } from "@preact/signals";
import type { Store } from "../model/store";
import type { SecretOffer } from "../protocol/generated";
import { describe } from "../model/errors";
import { keepYours, offerCard, offerLine, offersLabel, useTheAgents, waitingOffers } from "../model/secretOffers";
import { ApprovalMark } from "./Approval";

function OfferCard({ store, offer, down }: { store: Store; offer: SecretOffer; down: boolean }) {
  const busy = useSignal(false);
  const problem = useSignal<string | null>(null);
  const act = (run: () => Promise<void>) => {
    busy.value = true;
    problem.value = null;
    void run().catch((error: unknown) => { problem.value = describe(error); }).finally(() => { busy.value = false; });
  };
  return (
    <li class="join-request" aria-label={`${offer.name}: ${offerLine(offer)}`}>
      <ApprovalMark />
      <div class="body">
        <span class="title"><strong>{offer.name}</strong></span>
        <span class="subtitle">{offerLine(offer)}</span>
        {problem.value && <span class="subtitle warning">{problem.value}</span>}
        {offer.conflict && (
          <div class="join-actions">
            <button type="button" disabled={down || busy.value} onClick={() => act(() => store.keepYourSecret(offer))}>{keepYours}</button>
            <button type="button" class="prominent" disabled={down || busy.value || !offer.mine}
              onClick={() => act(() => store.useAgentsSecret(offer))}>{useTheAgents}</button>
          </div>
        )}
      </div>
    </li>
  );
}

/** Every agent's secret that is not the person's yet, or nothing. */
export function SecretOffers({ store, down }: { store: Store; down: boolean }) {
  const offers = waitingOffers(store.secretOffers.value);
  if (offers.length === 0) return null;
  return (
    <section class="join-requests" aria-label={offersLabel}>
      <ul>{offers.map((offer) => <OfferCard key={offerCard(offer)} store={store} offer={offer} down={down} />)}</ul>
    </section>
  );
}
