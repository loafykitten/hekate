import { describe, expect, it } from "vitest";
import { asJournalDay } from "./model";
import { diaryDate } from "./journal";

const memory = (id: number, content: string) => ({ id, kind: "fact", content, importance: 3, follow_up_at: null, created_at: "", updated_at: "" });

describe("asJournalDay", () => {
  it("parses a day with its diary and changes, dropping malformed changes", () => {
    const day = asJournalDay({
      day: "2026-10-19",
      diary: { day: "2026-10-19", text: "A gentle day.", created_at: "x", updated_at: "y" },
      reflections: [
        {
          id: 3,
          summary: "Saved 1",
          created_at: "z",
          changes: [
            { id: 11, action: "update", memory_id: 42, before: memory(42, "Friday"), after: memory(42, "Thursday"), reason: "moved", undone_at: null },
            { id: 12, action: "retire", memory_id: 7, before: memory(7, "Likes oolong"), after: null, reason: "", undone_at: "2026-10-20T08:00:00Z" },
            { id: 13, action: "explode", memory_id: 1 },
          ],
        },
      ],
    });
    expect(day?.diary?.text).toBe("A gentle day.");
    expect(day?.reflections[0].changes).toHaveLength(2);
    expect(day?.reflections[0].changes[0]).toMatchObject({ id: "11", action: "update", memory_id: "42", reason: "moved", undone_at: null });
    expect(day?.reflections[0].changes[0].before?.content).toBe("Friday");
    expect(day?.reflections[0].changes[1]).toMatchObject({ after: null, undone_at: "2026-10-20T08:00:00Z" });
  });

  it("keeps a day without a diary entry and refuses one without a day", () => {
    expect(asJournalDay({ day: "2026-10-18", diary: null, reflections: [] })).toEqual({ day: "2026-10-18", diary: null, reflections: [] });
    expect(asJournalDay({ diary: null })).toBeUndefined();
  });
});

describe("diaryDate", () => {
  it("names the day without shifting it across zones", () => {
    expect(diaryDate("2026-10-19")).toMatch(/Monday/);
    expect(diaryDate("2026-10-19")).toMatch(/19/);
    expect(diaryDate("not a day")).toBe("not a day");
  });
});
