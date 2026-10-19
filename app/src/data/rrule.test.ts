import { describe, expect, it } from "vitest";
import { describeRrule, presetOf, ruleOf } from "./rrule";

describe("repeat presets", () => {
  it("maps presets to rules and back", () => {
    for (const preset of ["daily", "weekdays", "weekly", "monthly"] as const) {
      expect(presetOf(ruleOf(preset))).toBe(preset);
    }
    expect(ruleOf("none")).toBeNull();
    expect(presetOf(null)).toBe("none");
  });

  it("recognises a preset written differently", () => {
    expect(presetOf("RRULE:FREQ=WEEKLY;BYDAY=FR,MO,TU,WE,TH")).toBe("weekdays");
    expect(presetOf("freq=daily")).toBe("daily");
  });

  it("leaves other rules as custom", () => {
    expect(presetOf("FREQ=WEEKLY;BYDAY=MO")).toBeUndefined();
    expect(presetOf("FREQ=DAILY;INTERVAL=2")).toBeUndefined();
  });
});

describe("describeRrule", () => {
  it("describes presets and common custom rules", () => {
    expect(describeRrule("FREQ=DAILY")).toBe("Every day");
    expect(describeRrule("FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR")).toBe("Weekdays");
    expect(describeRrule("FREQ=WEEKLY;BYDAY=MO,TH")).toBe("Every Mon, Thu");
    expect(describeRrule("FREQ=DAILY;INTERVAL=3")).toBe("Every 3 days");
    expect(describeRrule("FREQ=YEARLY")).toBe("Every year");
    expect(describeRrule("FREQ=SECONDLY")).toBe("Repeats");
    expect(describeRrule(null)).toBe("");
  });
});
