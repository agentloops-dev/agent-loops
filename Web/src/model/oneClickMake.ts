// New Droplet (#6577, epic #6070 W3 to W5), as the window's OneClickMake: on a connected DigitalOcean provider, one click
// makes a machine with what New Machine would make (the region and size used last there, else the nearest region and the
// recommended size, #6073). The card says where and what it costs before the click; Change… opens the whole form.
import type { JSONValue, ProviderOptions, ProviderSummary } from "../protocol/generated";

/** The preset whose card offers it: DigitalOcean Droplets over its API. */
export const oneClickPreset = "digitalocean-api";
export const oneClickWords = {
  button: "New Droplet",
  change: "Change…",
  openMachine: "Open Machine",
  ready: (name: string) => `${name} is ready. Agents can work on it now.`,
  note: (name: string) =>
    `New Droplet makes ${name} there. DigitalOcean charges for it while it exists, also when it is off: delete it to stop paying.`,
};

/** Whether the provider's card offers New Droplet. */
export function offersOneClick(provider: ProviderSummary): boolean {
  return provider.preset === oneClickPreset && provider.capabilities.includes("create");
}

export interface OneClickPlan {
  name: string;
  values: Record<string, JSONValue>;
  /** Where and what, with the monthly price: "San Francisco 3 · 2 vCPUs · 4 GB · 80 GB disk · $24/month". */
  summary: string;
}

type Property = { enum?: unknown[]; default?: JSONValue; "x-titles"?: Record<string, unknown>; "x-costs"?: Record<string, unknown> };

/** The plan from the provider's New Machine form (`providers/options`): its values, each enum on its first choice where
 * none is given, named by the form's `x-titles`. Null when the form gives no name. */
export function oneClickPlan(options: ProviderOptions): OneClickPlan | null {
  if (!options.name) return null;
  const properties = ((options.schema as { properties?: Record<string, Property> } | null)?.properties) ?? {};
  const given = options.values && typeof options.values === "object" && !Array.isArray(options.values)
    ? options.values as Record<string, JSONValue> : {};
  const values: Record<string, JSONValue> = { ...given };
  for (const [key, p] of Object.entries(properties)) {
    if (values[key] !== undefined && values[key] !== null) continue;
    delete values[key];
    if (Array.isArray(p.enum) && p.enum.length > 0) values[key] = p.enum[0] as JSONValue;
    else if (p.default !== undefined) values[key] = p.default;
  }
  const parts: string[] = [];
  for (const key of ["region", "size"]) {
    const chosen = values[key];
    if (typeof chosen !== "string") continue;
    const title = properties[key]?.["x-titles"]?.[chosen];
    const cost = properties[key]?.["x-costs"]?.[chosen];
    parts.push(typeof title === "string" ? title : typeof cost === "string" ? `${chosen} · ${cost}` : chosen);
  }
  return { name: options.name, values, summary: parts.join(" · ") };
}
