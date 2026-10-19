export const SECTIONS = ["today", "tasks", "reminders", "calendar", "memory", "settings"] as const;
export type Section = (typeof SECTIONS)[number];

export const SECTION_LABEL: Record<Section, string> = {
  today: "Today",
  tasks: "Tasks",
  reminders: "Reminders",
  calendar: "Calendar",
  memory: "Memory",
  settings: "Settings",
};

export function isSection(value: unknown): value is Section {
  return typeof value === "string" && (SECTIONS as readonly string[]).includes(value);
}
