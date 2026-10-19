// The repeat choices the menu offers, mapped to RFC 5545 RRULE strings. Rules made elsewhere
// (by Renamon, or a calendar app) that aren't one of these are kept as they are.

export type RepeatPreset = "none" | "daily" | "weekdays" | "weekly" | "monthly";

export const PRESET_RULES: Record<Exclude<RepeatPreset, "none">, string> = {
  daily: "FREQ=DAILY",
  weekdays: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR",
  weekly: "FREQ=WEEKLY",
  monthly: "FREQ=MONTHLY",
};

export const PRESET_LABEL: Record<RepeatPreset, string> = {
  none: "Doesn't repeat",
  daily: "Every day",
  weekdays: "Weekdays",
  weekly: "Every week",
  monthly: "Every month",
};

export const PRESETS: RepeatPreset[] = ["none", "daily", "weekdays", "weekly", "monthly"];

function normalize(rrule: string): string {
  return rrule
    .replace(/^RRULE:/i, "")
    .split(";")
    .map((p) => p.trim().toUpperCase())
    .filter(Boolean)
    // Day lists compare the same in any order.
    .map((p) => (p.startsWith("BYDAY=") ? `BYDAY=${p.slice(6).split(",").sort().join(",")}` : p))
    .sort()
    .join(";");
}

// The preset a stored rule corresponds to, or undefined for a custom one.
export function presetOf(rrule: string | null | undefined): RepeatPreset | undefined {
  if (!rrule) return "none";
  const n = normalize(rrule);
  for (const preset of PRESETS) {
    if (preset !== "none" && normalize(PRESET_RULES[preset]) === n) return preset;
  }
  return undefined;
}

export function ruleOf(preset: RepeatPreset): string | null {
  return preset === "none" ? null : PRESET_RULES[preset];
}

const DAY_NAME: Record<string, string> = { MO: "Mon", TU: "Tue", WE: "Wed", TH: "Thu", FR: "Fri", SA: "Sat", SU: "Sun" };

// A short phrase for the repeat indicator: "Every day", "Weekdays", "Every Mon, Thu", "Repeats".
export function describeRrule(rrule: string | null | undefined): string {
  if (!rrule) return "";
  const preset = presetOf(rrule);
  if (preset && preset !== "none") return PRESET_LABEL[preset];
  const parts = Object.fromEntries(
    normalize(rrule)
      .split(";")
      .map((p) => p.split("=") as [string, string]),
  );
  const interval = Number(parts.INTERVAL ?? 1);
  const unit = { DAILY: "day", WEEKLY: "week", MONTHLY: "month", YEARLY: "year" }[parts.FREQ as string];
  if (parts.FREQ === "WEEKLY" && parts.BYDAY && interval === 1) {
    const days = parts.BYDAY.split(",").map((d) => DAY_NAME[d.replace(/^[-+]?\d+/, "")] ?? d);
    return `Every ${days.join(", ")}`;
  }
  if (unit) return interval > 1 ? `Every ${interval} ${unit}s` : `Every ${unit}`;
  return "Repeats";
}
