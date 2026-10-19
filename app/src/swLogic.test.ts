import { describe, expect, it } from "vitest";
import { clickTarget, isCharacterAsset, parsePushPayload } from "./swLogic";

describe("parsePushPayload", () => {
  it("reads the service's JSON payload", () => {
    expect(parsePushPayload('{"title":"Reminder","body":"Call the dentist","url":"/?menu=reminders","tag":"r7"}')).toEqual({
      title: "Reminder",
      body: "Call the dentist",
      url: "/?menu=reminders",
      tag: "r7",
    });
  });

  it("falls back to sensible defaults for missing fields", () => {
    expect(parsePushPayload("{}")).toEqual({ title: "Hekate", body: "", url: "/", tag: undefined });
    expect(parsePushPayload('{"title":5,"tag":""}')).toMatchObject({ title: "Hekate", tag: undefined });
  });

  it("still shows something for empty, plain-text and broken payloads", () => {
    expect(parsePushPayload(undefined)).toMatchObject({ title: "Hekate", url: "/" });
    expect(parsePushPayload("")).toMatchObject({ title: "Hekate" });
    expect(parsePushPayload("hello there")).toMatchObject({ title: "Hekate", body: "hello there" });
    expect(parsePushPayload("[1,2]")).toMatchObject({ title: "Hekate", body: "" });
  });
});

describe("clickTarget", () => {
  const origin = "https://hekate.example";
  it("keeps same-origin urls, as a path", () => {
    expect(clickTarget("/?menu=tasks", origin)).toBe("/?menu=tasks");
    expect(clickTarget("https://hekate.example/a/b?x=1#h", origin)).toBe("/a/b?x=1#h");
  });

  it("never leaves the app", () => {
    expect(clickTarget("https://evil.example/phish", origin)).toBe("/");
    expect(clickTarget("javascript:alert(1)", origin)).toBe("/");
    expect(clickTarget(undefined, origin)).toBe("/");
  });
});

describe("isCharacterAsset", () => {
  it("matches the manifest and the GLBs only", () => {
    expect(isCharacterAsset("/character/manifest.json")).toBe(true);
    expect(isCharacterAsset("/character/renamon-phone.glb")).toBe(true);
    expect(isCharacterAsset("/character/other.json")).toBe(false);
    expect(isCharacterAsset("/api/character/renamon.glb")).toBe(false);
    expect(isCharacterAsset("/character/../secret.glb")).toBe(false);
  });
});
