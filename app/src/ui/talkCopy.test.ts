import { describe, expect, it } from "vitest";
import { talkCopy } from "./talkCopy";

describe("talkCopy", () => {
  it("disables the button offline and when speech-to-text is unavailable", () => {
    expect(talkCopy("idle", false, false, undefined)).toMatchObject({ disabled: true, hint: "Offline" });
    expect(talkCopy("idle", false, true, false)).toMatchObject({ disabled: true });
    expect(talkCopy("idle", false, true, false).hint).toMatch(/type/i);
  });

  it("describes each state, and tells a latched recording apart from a held one", () => {
    expect(talkCopy("idle", false, true, true)).toMatchObject({ disabled: false, hint: "Hold to talk" });
    expect(talkCopy("listening", false, true, true).hint).toBe("Release to send");
    expect(talkCopy("listening", true, true, true).hint).toMatch(/Tap to send/);
    expect(talkCopy("transcribing", false, true, true).disabled).toBe(true);
    expect(talkCopy("speaking", false, true, true).disabled).toBe(false);
    expect(talkCopy("thinking", false, true, true).disabled).toBe(false);
  });
});
