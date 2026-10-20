import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it } from "vitest";
import { applyApproval, approvalKindLabel, freshest, patchPending, patchRecent } from "./approvals";
import { asApproval, type Approval } from "./model";
import { decideBody, MAX_DENY_NOTE } from "./rest";
import { keys } from "./queryKeys";

const make = (id: string, status: Approval["status"] = "pending", extra: Partial<Approval> = {}): Approval => ({
  id,
  kind: "mail_trash",
  title: `Approval ${id}`,
  detail: "",
  scope_label: "mail from example.com",
  status,
  result: null,
  decided_by: null,
  note: null,
  created_at: "2026-10-19T08:00:00Z",
  expires_at: null,
  decided_at: null,
  ...extra,
});

describe("approval cache patches", () => {
  it("adds a new pending approval at the front and drops it once decided", () => {
    expect(patchPending([make("1")], make("2")).map((a) => a.id)).toEqual(["2", "1"]);
    expect(patchPending([make("1"), make("2")], make("1", "done")).map((a) => a.id)).toEqual(["2"]);
    expect(patchPending([make("1")], make("1", "running"))).toEqual([]);
  });

  it("keeps recent newest decision first", () => {
    const old = make("1", "done", { decided_at: "2026-10-18T08:00:00Z" });
    const next = make("2", "denied", { decided_at: "2026-10-19T09:00:00Z" });
    expect(patchRecent([old], next).map((a) => a.id)).toEqual(["2", "1"]);
    expect(patchRecent([old], make("1", "pending"))).toEqual([]);
  });

  it("lets a later state beat an earlier one", () => {
    expect(freshest(make("1"), make("1", "running")).status).toBe("running");
    expect(freshest(make("1", "done"), make("1", "running")).status).toBe("done");
    expect(freshest(make("1", "running"), make("1", "failed")).status).toBe("failed");
    expect(freshest(make("1", "done"), make("1")).status).toBe("done");
  });
});

describe("applyApproval", () => {
  it("updates the item and both lists in the query cache", () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.approvals("pending"), [make("1"), make("2")]);
    qc.setQueryData(keys.approvals("recent"), []);
    qc.setQueryData(keys.approval("1"), make("1"));
    const done = make("1", "done", { result: "Moved 14 messages to Trash", decided_at: "2026-10-19T09:00:00Z" });
    applyApproval(qc, done);
    expect(qc.getQueryData<Approval[]>(keys.approvals("pending"))?.map((a) => a.id)).toEqual(["2"]);
    expect(qc.getQueryData<Approval[]>(keys.approvals("recent"))).toEqual([done]);
    expect(qc.getQueryData(keys.approval("1"))).toEqual(done);
  });

  it("refreshes the change requests when a change request's approval changes", async () => {
    const qc = new QueryClient();
    qc.setQueryData(keys.changes, []);
    applyApproval(qc, make("4", "denied", { kind: "change_request", note: "Not now." }));
    expect(qc.getQueryState(keys.changes)?.isInvalidated).toBe(true);
    const other = new QueryClient();
    other.setQueryData(keys.changes, []);
    applyApproval(other, make("5", "done"));
    expect(other.getQueryState(keys.changes)?.isInvalidated).toBe(false);
  });

  it("leaves lists nobody has loaded alone", () => {
    const qc = new QueryClient();
    applyApproval(qc, make("3"));
    expect(qc.getQueryData(keys.approvals("pending"))).toBeUndefined();
    expect(qc.getQueryData(keys.approval("3"))).toMatchObject({ id: "3" });
  });
});

describe("approvalKindLabel", () => {
  it("names known actions and tidies unknown ones", () => {
    expect(approvalKindLabel("mail_trash")).toBe("Move mail to Trash");
    expect(approvalKindLabel("calendar_delete")).toBe("Calendar delete");
    expect(approvalKindLabel("")).toBe("Action");
  });
});

describe("denial notes", () => {
  it("reads a note off an approval and treats a missing or empty one as none", () => {
    const base = { id: "5", title: "Build: Match mail rules on subject words?", status: "denied" };
    expect(asApproval({ ...base, note: "Too broad" })?.note).toBe("Too broad");
    expect(asApproval({ ...base, note: "" })?.note).toBeNull();
    expect(asApproval(base)?.note).toBeNull();
  });

  it("sends a note only with deny, trimmed and capped, and omits an empty one", () => {
    expect(decideBody("deny", "  not now ")).toEqual({ decision: "deny", note: "not now" });
    expect(decideBody("deny", "   ")).toEqual({ decision: "deny" });
    expect(decideBody("deny")).toEqual({ decision: "deny" });
    expect(decideBody("once", "ignored")).toEqual({ decision: "once" });
    expect(decideBody("deny", "x".repeat(900)).note).toHaveLength(MAX_DENY_NOTE);
  });
});
