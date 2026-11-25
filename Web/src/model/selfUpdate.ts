// How a machine updates Agent Loops by itself (#6114): the words, the same as AgentsKit's SelfUpdateWords on the Mac.
import type { SelfUpdate } from "../protocol/generated";

/** The row's label: the Mac's switch says the same. */
export const automaticUpdates = "Update Agent Loops automatically";

/** The row's value: a machine that cannot update itself does not, whatever its switch says. */
export const updateValue = (update: SelfUpdate) => update.autoUpdate && update.state !== "cannot" ? "On" : "Off";

/** Where its update is. */
export function updateLine(update: SelfUpdate): string {
  const release = update.release ?? "";
  switch (update.state) {
    case "off": return release === "" ? "Off." : `Off. Your control plane runs ${release}.`;
    case "downloading": return `Downloading ${release}…`;
    case "waiting": return `${release} is ready. It installs when no agent is in a turn.`;
    case "restarting": return `Installed ${release}. Restarting…`;
    case "failed": case "cannot": return update.message ?? "Agent Loops cannot update itself.";
    default: return "Up to date.";
  }
}

/** Whether the line is in the failure tint. */
export const updateFailed = (update: SelfUpdate) => update.state === "failed";

/** The line when the machine did not answer. */
export const updateNoAnswer = (name: string) => `${name} can’t be asked about its updates now.`;
