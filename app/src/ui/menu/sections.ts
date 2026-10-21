export const SECTIONS = ["today", "tasks", "reminders", "calendar", "mail", "money", "changes", "memory", "settings"] as const;
export type Section = (typeof SECTIONS)[number];

export const SECTION_LABEL: Record<Section, string> = {
  today: "Today",
  tasks: "Tasks",
  reminders: "Reminders",
  calendar: "Calendar",
  mail: "Mail",
  money: "Money",
  changes: "Changes",
  memory: "Memory",
  settings: "Settings",
};

export function isSection(value: unknown): value is Section {
  return typeof value === "string" && (SECTIONS as readonly string[]).includes(value);
}
