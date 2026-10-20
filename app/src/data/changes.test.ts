import { describe, expect, it } from "vitest";
import { canRetry, changeKindLabel, changeStatusLabel, changeStatusTone, changeSummary } from "./changes";
import { asChangeRequest, type ChangeRequest } from "./model";

const raw = {
  id: "3",
  title: "Match mail rules on subject words",
  what: "Let a rule match words in the subject.",
  why: "Newsletters share subject words, not senders.",
  kind: "feature",
  revises: null,
  status: "queued",
  approval_id: "41",
  feedback: null,
  stage: "",
  issue_url: null,
  pr_url: null,
  result: null,
  attempts: 0,
  previous_result: null,
  previous_pr_url: null,
  created_at: "2026-10-19T08:00:00Z",
  updated_at: "2026-10-19T08:00:00Z",
  finished_at: null,
};

const make = (over: Partial<ChangeRequest> = {}): ChangeRequest => ({ ...(asChangeRequest(raw) as ChangeRequest), ...over });

describe("asChangeRequest", () => {
  it("reads a full change request", () => {
    expect(asChangeRequest(raw)).toEqual(raw);
  });

  it("accepts numeric ids and fills in missing optional fields", () => {
    const c = asChangeRequest({ id: 7, title: "Fix it", status: "proposed", revises: 3, approval_id: 9 });
    expect(c).toMatchObject({ id: "7", revises: "3", approval_id: "9", what: "", why: "", stage: "", feedback: null, result: null });
  });

  it("drops entries it can't show", () => {
    expect(asChangeRequest(null)).toBeUndefined();
    expect(asChangeRequest({ ...raw, id: "" })).toBeUndefined();
    expect(asChangeRequest({ ...raw, title: 4 })).toBeUndefined();
    expect(asChangeRequest({ ...raw, status: "exploded" })).toBeUndefined();
  });

  it("keeps only GitHub links", () => {
    const c = asChangeRequest({ ...raw, issue_url: "https://github.com/example/hekate/issues/4", pr_url: "https://evil.example.com/pr/1" });
    expect(c?.issue_url).toBe("https://github.com/example/hekate/issues/4");
    expect(c?.pr_url).toBeNull();
    expect(asChangeRequest({ ...raw, pr_url: "javascript:alert(1)" })?.pr_url).toBeNull();
    expect(asChangeRequest({ ...raw, pr_url: "http://github.com/example/hekate/pull/1" })?.pr_url).toBeNull();
  });
});

describe("change wording", () => {
  it("labels and tones every status", () => {
    expect(changeStatusLabel("merged")).toBe("Merged");
    expect(changeStatusTone("merged")).toBe("ok");
    expect(changeStatusTone("failed")).toBe("danger");
    expect(changeStatusTone("working")).toBe("accent");
    expect(changeStatusTone("denied")).toBeUndefined();
  });

  it("tidies kinds", () => {
    expect(changeKindLabel("improvement")).toBe("Improvement");
    expect(changeKindLabel("")).toBe("Change");
  });

  it("summarises where a change stands", () => {
    expect(changeSummary(make({ status: "working", stage: "PR open, in review" }))).toBe("PR open, in review");
    expect(changeSummary(make({ status: "denied", feedback: "Too broad" }))).toBe("Too broad");
    expect(changeSummary(make({ status: "merged", result: "Shipped." }))).toBe("Shipped.");
    expect(changeSummary(make({ status: "failed", result: null, stage: "Building" }))).toBe("Building");
    expect(changeSummary(make({ status: "queued" }))).toBe("");
  });
});

describe("retrying", () => {
  it("offers a retry only for what failed or was turned down", () => {
    expect(canRetry(make({ status: "failed", attempts: 1 }))).toBe(true);
    expect(canRetry(make({ status: "rejected", attempts: 2 }))).toBe(true);
    // A proposal whose approval failed reads "failed" too, but nobody ever took it up.
    expect(canRetry(make({ status: "failed", attempts: 0 }))).toBe(false);
    expect(canRetry(make({ status: "merged", attempts: 1 }))).toBe(false);
    expect(canRetry(make({ status: "working", attempts: 1 }))).toBe(false);
  });

  it("says a retried change is back on the list", () => {
    expect(changeSummary(make({ status: "queued", attempts: 1 }))).toBe("Back on the list");
    expect(changeSummary(make({ status: "queued", attempts: 0 }))).toBe("");
  });

  it("reads the last attempt, keeping only GitHub links", () => {
    const c = asChangeRequest({ ...raw, attempts: 1, previous_result: "Three rounds weren't enough.", previous_pr_url: "https://github.com/example/hekate/pull/15" });
    expect(c).toMatchObject({ attempts: 1, previous_result: "Three rounds weren't enough.", previous_pr_url: "https://github.com/example/hekate/pull/15" });
    expect(asChangeRequest({ ...raw, previous_pr_url: "https://evil.example/pr" })?.previous_pr_url).toBeNull();
  });
});
