import { useQuery } from "@tanstack/react-query";

export const HEALTH_POLL_MS = 15_000;

export interface Health {
  ok: boolean;
  version: string;
}

// Service health. Throws when the service can't be reached or answers with something that
// isn't its JSON (an SPA fallback page, a proxy error), so callers see those as offline.
export async function fetchHealth(signal?: AbortSignal): Promise<Health> {
  const res = await fetch("/healthz", { cache: "no-store", signal: AbortSignal.any([AbortSignal.timeout(5000), ...(signal ? [signal] : [])]) });
  // The service answers 503 with the same JSON shape when it is up but unhealthy.
  if (!res.ok && res.status !== 503) throw new Error(`healthz: ${res.status}`);
  const body = (await res.json()) as { ok?: unknown; version?: unknown };
  if (typeof body.ok !== "boolean") throw new Error("healthz: unexpected response");
  return { ok: body.ok, version: typeof body.version === "string" ? body.version : "" };
}

export type ConnectionState =
  | { kind: "checking" }
  | { kind: "connected"; version: string }
  | { kind: "degraded"; version: string }
  | { kind: "offline" };

export function useConnection(): ConnectionState {
  const q = useQuery({
    queryKey: ["healthz"],
    queryFn: ({ signal }) => fetchHealth(signal),
    refetchInterval: HEALTH_POLL_MS,
    retry: false,
    networkMode: "always", // an unreachable service is the answer here, not a reason to pause
  });
  if (q.isError) return { kind: "offline" };
  if (!q.data) return { kind: "checking" };
  return { kind: q.data.ok ? "connected" : "degraded", version: q.data.version };
}
