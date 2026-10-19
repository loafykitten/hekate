const KIND_LABEL: Record<string, string> = {
  fact: "Fact",
  preference: "Preference",
  person: "Person",
  event: "Event",
  follow_up: "Follow-up",
};

export const kindLabel = (kind: string): string => KIND_LABEL[kind] ?? kind;
