import type { ReviewFinding, Severity, ThreadModel } from "../../data/model";

export const MODEL_LABEL: Record<ThreadModel, string> = { deep: "GLM 5.3", chat: "Flash" };

const SEVERITY_RANK: Record<Severity, number> = { high: 0, medium: 1, low: 2 };

// Worst first; findings of one severity keep the order the reviewer gave them.
export function sortFindings(findings: readonly ReviewFinding[]): ReviewFinding[] {
  return [...findings].sort((a, b) => SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity]);
}

