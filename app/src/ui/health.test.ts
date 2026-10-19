import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchHealth } from "./health";

function respond(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({ ok: status >= 200 && status < 300, status, json: async () => body })),
  );
}

describe("fetchHealth", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("reads ok and version from the service", async () => {
    respond(200, { ok: true, version: "0.3.1", commit: "abc" });
    expect(await fetchHealth()).toEqual({ ok: true, version: "0.3.1" });
  });

  it("reports an unhealthy service (503 with the same shape) as not ok", async () => {
    respond(503, { ok: false, version: "0.3.1", db: "closed" });
    expect(await fetchHealth()).toEqual({ ok: false, version: "0.3.1" });
  });

  it("throws on proxy errors and on responses that aren't the service's JSON", async () => {
    respond(502, {});
    await expect(fetchHealth()).rejects.toThrow();
    respond(200, { hello: "an SPA fallback or some other server" });
    await expect(fetchHealth()).rejects.toThrow(/unexpected/);
  });

  it("throws when the network is down", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    await expect(fetchHealth()).rejects.toThrow();
  });
});
