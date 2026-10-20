// The morning briefing, phone and desktop: opening the app starts nothing on its own; typing "Good
// morning" gets the catch-up with its cards; Settings explains how to start it. The socket and REST
// are answered here with fixtures (the service decides the briefing; this plays its reply), and the
// script checks what the app sent. Usage: node scripts/verify/briefing.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const TEXT = "Morning, Rika. Rain until about two, so take the umbrella to the dentist at three. Rent is overdue, and the landlord wrote about the lease.";
const at = (h, m) => { const d = new Date(); d.setHours(h, m, 0, 0); return d.toISOString(); };
const weather = { place: "Home", unit: "F", now: { temp: 48, feels_like: 44, condition: "Rain", code: 61 }, today: { high: 55, low: 44, precip_chance: 80, sunrise: at(7, 20), sunset: at(18, 10) }, hours: [], days: [] };
const cards = [
  { kind: "weather", id: "weather", title: "Weather: Home", data: weather },
  { kind: "tasks", id: "tasks:due", title: "Due today", data: { items: [{ id: "1", title: "Pay rent", notes: "", list: "Inbox", due: "2026-10-19", rrule: null, priority: 0, completed_at: null, created_at: "", updated_at: "" }] } },
];
const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 390, height: 844 }], ["desktop", { width: 1280, height: 800 }]]) {
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  page.on("pageerror", (e) => console.log(`${vname} pageerror:`, e.message));
  const sent = [];
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    ws.send(JSON.stringify({ type: "ready", client_id: "c1", conversation_id: "k", capabilities: { stt: true, tts: false }, gestures: ["waving"], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }));
    ws.onMessage((raw) => {
      if (typeof raw !== "string") return;
      const m = JSON.parse(raw);
      sent.push(m);
      if (m.type !== "say") return;
      ws.send(JSON.stringify({ type: "turn_start", turn_id: "tb", input_id: m.id, origin: "c1", user_text: m.text }));
      ws.send(JSON.stringify({ type: "activity", turn_id: "tb", tool: "morning_briefing", label: "Looking over your day", state: "start" }));
      ws.send(JSON.stringify({ type: "activity", turn_id: "tb", tool: "morning_briefing", label: "Looking over your day", state: "done" }));
      for (const card of cards) ws.send(JSON.stringify({ type: "card", turn_id: "tb", card }));
      ws.send(JSON.stringify({ type: "segment", turn_id: "tb", seq: 0, text: TEXT, duration: 6, audio_b64: null, audio_format: null, words: null, gestures: [{ name: "waving", at: 0 }], expressions: [] }));
      ws.send(JSON.stringify({ type: "turn_end", turn_id: "tb", status: "done", text: TEXT, model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } }));
    });
  });
  await page.route("**/api/**", (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/routines") return json(route, { heartbeat: true, waking_start: "08:00", waking_end: "22:00" });
    if (p === "/api/weather") return json(route, weather);
    if (p === "/api/tasks") return json(route, { tasks: [] });
    if (p === "/api/reminders") return json(route, { reminders: [] });
    if (p === "/api/calendar/status") return json(route, { configured: false });
    if (p === "/api/approvals") return json(route, { approvals: [] });
    if (p === "/api/approvals/rules") return json(route, { rules: [] });
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    return route.fulfill({ status: 204 });
  });
  await page.goto(BASE);
  await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
  await page.waitForTimeout(2500);
  check(!sent.some((m) => m.type === "say" || m.type === "briefing"), `${vname}: opening the app started nothing`);

  await page.getByRole("button", { name: "Type instead" }).click();
  await page.getByRole("textbox", { name: "Message" }).fill("Good morning!");
  await page.getByRole("button", { name: "Send" }).click();
  await page.getByRole("button", { name: "Open Due today in the menu" }).waitFor({ timeout: 5000 });
  check(sent.some((m) => m.type === "say" && m.text === "Good morning!"), `${vname}: the greeting was sent as said`);
  check(await page.getByRole("button", { name: "Open Weather: Home in the menu" }).isVisible(), `${vname}: the briefing's cards are shown`);
  await page.waitForTimeout(600);
  await page.screenshot({ path: path.join(out, `briefing-${vname}-playing.png`) });
  await page.goto(BASE + "?menu=settings");
  await page.getByText("Morning briefing").scrollIntoViewIfNeeded();
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(out, `briefing-${vname}-settings.png`) });
  await page.close();
}
await browser.close();
