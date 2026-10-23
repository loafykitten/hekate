import { describe, expect, it } from "vitest";
import { claimsSpace, claimsTyping, isHoldKey, startsHoldTalk } from "./keys";

// A stand-in for an element whose ancestors (itself included) have these tag names / attributes.
function el(...matches: string[]): EventTarget {
  return {
    closest: (selector: string) =>
      selector
        .split(/,(?![^(]*\))/)
        .map((s) => s.trim())
        .some((s) => matches.some((m) => s === m || s.startsWith(`${m}:`) || s.startsWith(`${m}[`))),
  } as unknown as EventTarget;
}

const press = (over: Partial<Parameters<typeof startsHoldTalk>[0]> = {}) => ({
  key: "p",
  repeat: false,
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  shiftKey: false,
  target: el("body"),
  ...over,
});

describe("startsHoldTalk (hold P)", () => {
  it("starts on a bare P, whatever the caps lock", () => {
    expect(startsHoldTalk(press())).toBe(true);
    expect(startsHoldTalk(press({ key: "P" }))).toBe(true);
    expect(isHoldKey("P")).toBe(true);
    expect(isHoldKey("o")).toBe(false);
  });

  it("ignores other keys, repeats and modified presses", () => {
    expect(startsHoldTalk(press({ key: "o" }))).toBe(false);
    expect(startsHoldTalk(press({ repeat: true }))).toBe(false);
    for (const mod of ["ctrlKey", "metaKey", "altKey", "shiftKey"] as const) expect(startsHoldTalk(press({ [mod]: true }))).toBe(false);
  });

  it("leaves P to fields you can type into", () => {
    expect(startsHoldTalk(press({ target: el("textarea") }))).toBe(false);
    expect(startsHoldTalk(press({ target: el("input") }))).toBe(false);
    expect(startsHoldTalk(press({ target: el("select") }))).toBe(false);
    expect(startsHoldTalk(press({ target: el("[contenteditable]") }))).toBe(false);
  });

  it("works with a button focused, since P does nothing to one", () => {
    expect(startsHoldTalk(press({ target: el("button") }))).toBe(true);
    expect(startsHoldTalk(press({ target: null }))).toBe(true);
  });
});

describe("claimsSpace / claimsTyping", () => {
  it("Space is claimed by buttons, links and dialogs, but not the talk button", () => {
    expect(claimsSpace(el("button"))).toBe(true);
    expect(claimsSpace(el("a"))).toBe(true);
    expect(claimsSpace(el("[role=dialog]"))).toBe(true);
    expect(claimsSpace(el("#talk-button", "button"))).toBe(false);
    expect(claimsSpace(el("body"))).toBe(false);
    expect(claimsSpace(null)).toBe(false);
  });

  it("typing is claimed only by text entry", () => {
    expect(claimsTyping(el("textarea"))).toBe(true);
    expect(claimsTyping(el("button"))).toBe(false);
    expect(claimsTyping(el("[role=dialog]"))).toBe(false);
  });
});
