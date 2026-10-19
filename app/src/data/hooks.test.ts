import { MutationObserver } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { createQueryClient } from "./hooks";

const approval = (status: string, result: string | null) => ({
  id: "5",
  kind: "mail_trash",
  title: "Move 3 messages to Trash?",
  detail: "",
  scope_label: null,
  status,
  result,
  decided_by: "owner",
  created_at: "2026-10-19T14:00:00Z",
  expires_at: "2026-10-20T14:00:00Z",
  decided_at: "2026-10-19T14:01:00Z",
});

async function decide(response: unknown, meta?: Record<string, unknown>) {
  const onError = vi.fn();
  const qc = createQueryClient(onError);
  await new MutationObserver(qc, { mutationFn: async () => response, meta }).mutate();
  return onError;
}

describe("approval outcomes", () => {
  it("says so when an allowed action ran and failed", async () => {
    const onError = await decide(approval("failed", "the mail server could not be reached"), { reportsApprovalFailure: true });
    expect(onError).toHaveBeenCalledWith("That didn't work: the mail server could not be reached", "approval-failed-5");
  });

  it("stays quiet when it worked, or for other mutations", async () => {
    expect(await decide(approval("done", "Moved 3 messages"), { reportsApprovalFailure: true })).not.toHaveBeenCalled();
    expect(await decide(approval("failed", "nope"))).not.toHaveBeenCalled();
  });
});
