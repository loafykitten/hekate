import { describe, expect, it } from "vitest";
import { talkCopy } from "./talkCopy";

describe("talkCopy", () => {
  it("disables the button offline and when speech-to-text is unavailable", () => {
    expect(talkCopy("idle", false, undefined, "Tap")).toMatchObject({ disabled: true, hint: "Offline" });
    expect(talkCopy("idle", true, false, "Tap")).toMatchObject({ disabled: true });
    expect(talkCopy("idle", true, false, "Tap").hint).toMatch(/type/i);
  });

  it("describes each state of the toggle, in the device's own verb", () => {
    expect(talkCopy("idle", true, true, "Tap")).toMatchObject({ disabled: false, hint: "Tap to talk" });
    expect(talkCopy("idle", true, true, "Click").hint).toBe("Click to talk");
    expect(talkCopy("starting", true, true, "Tap")).toMatchObject({ disabled: false, hint: "Opening the mic…" });
    expect(talkCopy("listening", true, true, "Tap").hint).toBe("Tap again to end");
    expect(talkCopy("listening", true, true, "Click").hint).toBe("Click again to end");
    expect(talkCopy("speaking", true, true, "Click")).toMatchObject({ disabled: false, hint: "Click to interrupt" });
    expect(talkCopy("transcribing", true, true, "Tap").disabled).toBe(true);
    expect(talkCopy("thinking", true, true, "Tap").disabled).toBe(false);
  });
});
