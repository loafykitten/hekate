import type { Card } from "../../data/model";
import type { Section } from "../menu/sections";

// The menu section a card belongs to, if it has one (web results and pages don't).
export function sectionOfCard(card: Card): Section | undefined {
  switch (card.kind) {
    case "tasks":
      return "tasks";
    case "reminders":
      return "reminders";
    case "events":
      return "calendar";
    case "memory":
      return "memory";
    case "weather":
      return "today";
    default:
      return undefined;
  }
}
