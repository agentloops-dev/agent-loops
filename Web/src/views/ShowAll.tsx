// A per-machine section shows the three machines that matter most, then **Show all** (#5620, epic #5281): with a
// hundred machines, a row or a section for each one buries the page. The list's own order says which matter most.
import { nameLimit } from "../model/machineNames";

/** "Show all 12" under a section with more than three machines, "Show fewer" once open; nothing with three or fewer. */
export function ShowAll({ count, all, toggle }: { count: number; all: boolean; toggle: () => void }) {
  if (count <= nameLimit) return null;
  return <p class="hint"><button type="button" class="link" aria-expanded={all} onClick={toggle}>{all ? "Show fewer" : `Show all ${count}`}</button></p>;
}
