// The offline shell, phone and desktop: visit once online (the service worker installs, the menu
// data is fetched and stored), go offline, reload, and check the app still opens: the stage
// renders from the cache, the pill says Offline, the menu shows the last-known Today and Tasks
// data with an "Offline · as of" note, and an action says the service isn't reachable.
// Needs the production build served with the character files (`vite preview` does that):
//   npm run build && npx vite preview --port 5318 --strictPort
//   HEKATE_URL=http://localhost:5318/ node scripts/verify/offline.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { launch } from "./common.mjs";

const BASE = process.env.HEKATE_URL ?? "http://localhost:5318/";
const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const iso = (d) => d.toISOString();
const day = (n) => iso(new Date(Date.now() + n * 86_400_000)).slice(0, 10);
const at = (h, m) => { const d = new Date(); d.setHours(h, m, 0, 0); return iso(d); };
const task = (id, title, list, due, priority = 0) => ({ id, title, notes: "", list, due, rrule: null, priority, completed_at: null, created_at: iso(new Date()), updated_at: iso(new Date()) });
const FIXTURES = {
  "/api/tasks": { tasks: [task("1", "Pay rent", "Home", day(-1), 2), task("2", "Water the plants", "Home", day(0)), task("3", "Book dentist", "Errands", day(5))] },
  "/api/lists": { lists: [{ name: "Home", open_count: 2 }, { name: "Errands", open_count: 1 }] },
  "/api/reminders": { reminders: [{ id: "r1", text: "Call the landlord", at: at(23, 0), rrule: null, status: "scheduled", last_fired_at: null, created_at: iso(new Date()) }] },
  "/api/calendar/status": { configured: false },
  "/api/weather": { place: "Home", unit: "F", now: { temp: 48, feels_like: 44, condition: "Rain", code: 61 }, today: { high: 55, low: 44, precip_chance: 80, sunrise: at(7, 20), sunset: at(18, 10) }, hours: [], days: [] },
  "/api/approvals": { approvals: [] },
  "/api/approvals/rules": { rules: [] },
  "/api/conversation/messages": { messages: [] },
  "/api/routines": { heartbeat: true, waking_start: "08:00", waking_end: "22:00", checkin: true, checkin_at: "00:00" },
};

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 390, height: 844 }], ["desktop", { width: 1280, height: 800 }]]) {
  const context = await browser.newContext({ viewport, hasTouch: viewport.width < 600 });
  let offline = false;
  await context.route("**/healthz", (route) =>
    offline ? route.abort("internetdisconnected") : route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ ok: true, version: "test" }) }),
  );
  await context.route("**/api/**", (route) => {
    if (offline) return route.abort("internetdisconnected");
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/ws") return route.fallback();
    const body = FIXTURES[p];
    if (body) return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
    return route.fulfill({ status: 204 });
  });
  const page = await context.newPage();
  page.on("pageerror", (e) => console.log(`${vname} pageerror:`, e.message));
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    if (offline) return void ws.close();
    ws.send(JSON.stringify({ type: "ready", client_id: "c1", conversation_id: "k", capabilities: { stt: false, tts: false }, gestures: [], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }));
  });

  // Online visit: install the worker, load the character, fetch and store the menu data.
  await page.goto(BASE + "?menu=today");
  await page.waitForSelector("[data-stage=ready]", { timeout: 60_000 });
  await page.getByText("Pay rent").waitFor({ timeout: 10_000 });
  await page.waitForSelector('[role=status][data-state=connected]', { timeout: 15_000 });
  let cached = false;
  for (let i = 0; i < 60 && !cached; i++) {
    cached = await page.evaluate(async () => {
      if (!navigator.serviceWorker.controller) return false;
      const urls = (await (await caches.open("hekate-character")).keys()).map((r) => new URL(r.url).pathname);
      return urls.includes("/character/manifest.json") && urls.some((u) => u.endsWith(".glb"));
    });
    if (!cached) await page.waitForTimeout(500);
  }
  check(cached, `${vname}: the service worker cached the character manifest and model`);
  let stored = "";
  for (let i = 0; i < 20 && !stored.includes("Pay rent"); i++) {
    await page.waitForTimeout(500); // the persister writes at most once a second
    stored = await page.evaluate(() => localStorage.getItem("hekate-query-cache") ?? "");
  }
  check(stored.includes("Pay rent"), `${vname}: the menu data was stored`);
  check(!stored.includes('"mail","message"') && !stored.includes('"approvals","item"'), `${vname}: no single-message or single-approval queries were stored`);
  await page.screenshot({ path: path.join(out, `offline-${vname}-online.png`) });

  // Offline reload.
  offline = true;
  await context.setOffline(true);
  await page.goto(BASE + "?menu=today");
  await page.waitForSelector("[data-stage=ready]", { timeout: 60_000 });
  check(true, `${vname}: the stage reached ready offline`);
  await page.waitForSelector("[role=status][data-state=offline]", { timeout: 20_000 });
  check(true, `${vname}: the pill says Offline`);
  const notes = page.getByText(/^Offline · as of /);
  await notes.first().waitFor({ timeout: 10_000 });
  check(await page.getByText("Pay rent").isVisible(), `${vname}: Today shows the cached overdue task`);
  check(await page.getByText("Call the landlord").isVisible(), `${vname}: Today shows the cached reminder`);
  check(await notes.count() === 1, `${vname}: one calm offline note: "${await notes.first().textContent()}"`);
  await page.waitForTimeout(500);
  await page.screenshot({ path: path.join(out, `offline-${vname}-today.png`) });

  await page.getByRole("button", { name: "Tasks", exact: true }).click();
  await page.getByText("Book dentist").waitFor({ timeout: 5000 });
  check(await page.getByText("Water the plants").isVisible(), `${vname}: Tasks shows the cached tasks`);
  check(await page.getByText(/^Offline · as of /).count() === 1, `${vname}: Tasks has the offline note`);
  await page.getByRole("checkbox", { name: "Complete Book dentist" }).click();
  const toast = page.getByRole("alert").filter({ hasText: "Hekate isn't reachable." });
  await toast.first().waitFor({ timeout: 5000 });
  check(true, `${vname}: completing a task says Hekate isn't reachable`);
  check(await page.getByText("Book dentist").isVisible(), `${vname}: the task is still listed after the failed attempt`);
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(out, `offline-${vname}-tasks.png`) });
  await context.close();
}
await browser.close();
