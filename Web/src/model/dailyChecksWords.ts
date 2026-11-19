// The daily checks' control's words (#3355, #5853), as Shared/UI/DailyChecksControls.swift and
// AgentsKit's DailyChecksSettings.swift say them.

export const dailyChecksStates: { enabled: boolean; label: string }[] = [
  { enabled: false, label: "Off" },
  { enabled: true, label: "On" },
];

/** How often the checks can run, in hours: each divides the day evenly (#3395). */
export const checksEveryChoices = [2, 3, 4, 6, 8, 12];

/** How often, as a person reads it, such as "Every 4 hours". */
export function checksEveryLabel(hours: number): string {
  return `Every ${hours} hours`;
}

export const checksHint = "Every few hours, an agent checks the folders that had no work: "
  + "failing CI, pull requests that went stale or are in conflict, and issues no session has. "
  + "For Review lists what it finds. It only reads.";
