import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, calendarApi, fetchWeather, memoryApi, remindersApi, request, tasksApi, unreachable } from "./rest";

function respond(status: number, body?: unknown, ok = status >= 200 && status < 300) {
  const fetchMock = vi.fn<(url: string, init?: RequestInit) => Promise<unknown>>(async () => ({
    ok,
    status,
    json: async () => {
      if (body === undefined) throw new SyntaxError("no body");
      return body;
    },
  }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => vi.unstubAllGlobals());

describe("request", () => {
  it("sends JSON and builds the query string without empty values", async () => {
    const f = respond(200, { tasks: [] });
    await request("GET", "/tasks", { query: { list: "Groceries", status: undefined, q: "" } });
    expect(f.mock.calls[0][0]).toBe("/api/tasks?list=Groceries");
    await request("POST", "/tasks", { body: { title: "x" } });
    const init = f.mock.calls[1][1]!;
    expect(init.method).toBe("POST");
    expect(init.body).toBe('{"title":"x"}');
    expect((init.headers as Record<string, string>)["Content-Type"]).toBe("application/json");
  });

  it("returns undefined for 204", async () => {
    respond(204);
    expect(await request("DELETE", "/tasks/1")).toBeUndefined();
  });

  it("returns undefined for an empty 202, like Sync now's", async () => {
    respond(202);
    expect(await request("POST", "/money/sync")).toBeUndefined();
  });

  it("throws the service's error message with the status", async () => {
    respond(400, { error: "title is required" });
    await expect(request("POST", "/tasks", { body: {} })).rejects.toMatchObject({ message: "title is required", status: 400 });
  });

  it("explains non-JSON answers (an SPA fallback or a proxy error)", async () => {
    respond(200, undefined);
    await expect(request("GET", "/tasks")).rejects.toThrow(/unexpected/);
    respond(500, undefined, false);
    await expect(request("GET", "/tasks")).rejects.toMatchObject({ message: "Hekate answered 500.", status: 500 });
  });

  it("says so when the service can't be reached", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => Promise.reject(new TypeError("Failed to fetch"))));
    await expect(request("GET", "/tasks")).rejects.toMatchObject({ message: "Hekate isn't reachable.", status: 0 });
  });

  it("says the same when the proxy finds the service down", async () => {
    for (const status of [502, 503, 504]) {
      respond(status); // an empty or HTML proxy page: no JSON
      const err = await request("GET", "/tasks").catch((e: unknown) => e);
      expect(err).toMatchObject({ message: "Hekate isn't reachable." });
      expect(unreachable(err)).toBe(true);
    }
  });

  it("keeps the service's own words for a failed integration, even on a gateway status", async () => {
    for (const [status, error] of [[502, "The calendar server refused the login."], [503, "Mail isn't set up."], [504, "The mail server took too long."]] as const) {
      respond(status, { error });
      const err = await request("GET", "/calendar/events").catch((e: unknown) => e);
      expect(err).toMatchObject({ message: error, status });
      expect(unreachable(err)).toBe(false);
    }
  });
});

describe("typed endpoints", () => {
  it("reads task lists and drops bad entries", async () => {
    respond(200, { tasks: [{ id: "1", title: "Milk" }, { nope: true }] });
    expect(await tasksApi.list({ status: "open" })).toMatchObject([{ id: "1", title: "Milk", list: "Inbox" }]);
  });

  it("treats a null array as empty", async () => {
    respond(200, { reminders: null });
    expect(await remindersApi.list("upcoming")).toEqual([]);
  });

  it("encodes ids and keys into the path", async () => {
    const f = respond(204);
    await memoryApi.removeCore("favorite tea/1");
    expect(f.mock.calls[0][0]).toBe("/api/memory/core/favorite%20tea%2F1");
    await calendarApi.remove("a@b.c");
    expect(f.mock.calls[1][0]).toBe("/api/calendar/events/a%40b.c");
  });

  it("reads calendar status", async () => {
    respond(200, { configured: true, calendar_name: "Home" });
    expect(await calendarApi.status()).toEqual({ configured: true, calendar_name: "Home" });
    respond(200, { configured: false });
    expect(await calendarApi.status()).toEqual({ configured: false, calendar_name: undefined });
  });

  it("returns null weather when no place is known, but still throws other errors", async () => {
    respond(404, { error: "no place" });
    expect(await fetchWeather()).toBeNull();
    respond(500, { error: "boom" });
    await expect(fetchWeather()).rejects.toBeInstanceOf(ApiError);
  });
});

describe("tasksApi.complete", () => {
  it("sends the occurrence being completed, so a repeat can't skip the next one", async () => {
    const task = { id: "4", title: "Recycling", notes: "", list: "Chores", due: "2026-10-19", rrule: "FREQ=WEEKLY", priority: 0, completed_at: null, created_at: "x", updated_at: "x" };
    const fetchMock = respond(200, task);
    await tasksApi.complete("4", "2026-10-19");
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toContain("/api/tasks/4/complete");
    expect(JSON.parse(String(init?.body))).toEqual({ due: "2026-10-19" });
  });
});
