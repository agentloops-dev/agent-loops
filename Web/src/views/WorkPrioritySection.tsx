// The item page's Priority section (#6093), as WorkPrioritySection on the window (#6092, epic #6086): an issue's
// priority as a menu (High, Medium, None, Low) and its On hold as a toggle, each where the forge sets it (the item's
// actions have priority or hold, #6091) and a machine of the person's writes; else the chip and, when it is on hold, the
// words. A choice writes at once (forge/priority, forge/hold), with a spinner while it runs; a failed write puts the old
// value back and says why under the section. The page reads the item again after a write (`wrote`), so the chip follows.
import { useSignal } from "@preact/signals";
import type { ForgeItemDetail } from "../protocol/generated";
import type { Store } from "../model/store";
import { describe } from "../model/errors";
import {
  holdLine, holdToggleHelp, holdToggleWords, onHoldWords, priorityChoiceWords, priorityChoices, priorityHelp, priorityTitle, setsPriority,
} from "../model/forgeNextOrder";
import { openContextMenu, type MenuItem } from "./ContextMenu";
import { Icon } from "./Icon";
import { PriorityChip } from "./WorkParts";
import { SideSection } from "./Work";

type Priority = ForgeItemDetail["item"]["priority"] | undefined;

export function WorkPrioritySection({ store, host, detail, writes, wrote }: {
  store: Store; host: string; detail: ForgeItemDetail; writes: boolean; wrote: () => Promise<void>;
}) {
  // The value a write in flight sets, shown until the read after it; undefined when none runs.
  const settingPriority = useSignal<{ to: Priority } | undefined>(undefined);
  const settingHold = useSignal<boolean | undefined>(undefined);
  const problem = useSignal<string | null>(null);
  const item = detail.item;
  const sets = setsPriority(detail.actions, writes);
  const priority: Priority = settingPriority.value ? settingPriority.value.to : item.priority;
  const onHold = settingHold.value ?? item.onHold === true;
  const busy = settingPriority.value !== undefined || settingHold.value !== undefined;

  const write = async (method: "forge/priority" | "forge/hold", params: object) => {
    problem.value = null;
    try {
      await store.link.call(method, params as never, host);
      await wrote();
    } catch (e) {
      // The old value back, and why under the section.
      problem.value = describe(e);
    } finally {
      settingPriority.value = undefined;
      settingHold.value = undefined;
    }
  };
  const setPriority = (to: Priority) => {
    if (to === priority || busy) return;
    settingPriority.value = { to };
    void write("forge/priority", to ? { node: item.node, priority: to } : { node: item.node });
  };
  const setHold = (on: boolean) => {
    if (on === onHold || busy) return;
    settingHold.value = on;
    void write("forge/hold", { node: item.node, on });
  };
  const menu = (e: MouseEvent) => openContextMenu(e, priorityChoices.map((choice): MenuItem => ({
    label: priorityChoiceWords(choice), checked: choice === priority, run: () => setPriority(choice),
  })));

  return (
    <SideSection title={priorityTitle}>
      <div class="work-priority-section">
        {sets.priority ? (
          <button type="button" class="work-priority-menu" disabled={busy} onClick={menu} title={priorityHelp}
            aria-label={`${priorityTitle}: ${priorityChoiceWords(priority)}`} aria-haspopup="menu">
            {priority ? <PriorityChip priority={priority} /> : <span class="quiet">None</span>}
            {settingPriority.value ? <span class="spinner" aria-hidden="true" /> : <Icon name="chevron-down" />}
          </button>
        ) : priority ? <PriorityChip priority={priority} /> : <span class="quiet">None</span>}
        {sets.hold ? (
          <div class="work-hold" title={holdToggleHelp}>
            <label>
              <input type="checkbox" checked={onHold} disabled={busy} onChange={(e) => setHold(e.currentTarget.checked)} />
              {holdToggleWords}
              {settingHold.value !== undefined && <span class="spinner" aria-hidden="true" />}
            </label>
            {onHold && <span class="quiet small">{holdLine}</span>}
          </div>
        ) : onHold && <span title={holdLine}>{onHoldWords}</span>}
        {problem.value && <span class="hint failure small">{problem.value}</span>}
      </div>
    </SideSection>
  );
}
