import { describe, expect, it } from "vitest";
import type { ReviewFinding } from "../../data/model";
import { sortFindings } from "./labels";

const f = (title: string, severity: ReviewFinding["severity"]): ReviewFinding => ({ severity, title, claim: "", evidence: "", fix: "", unverified: false, commands: [] });

describe("sortFindings", () => {
  it("orders high to low and keeps the reviewer's order within a severity", () => {
    const sorted = sortFindings([f("a", "low"), f("b", "high"), f("c", "medium"), f("d", "high")]);
    expect(sorted.map((x) => x.title)).toEqual(["b", "d", "c", "a"]);
  });
});
