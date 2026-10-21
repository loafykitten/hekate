// The evening check-in, phone and desktop: opening the app from its notification (`?checkin=day`)
// starts it once, with nothing said by the owner; the in-app notice's "Talk it over" starts it too;
// Settings has its switch and time. The socket and REST are answered here with fixtures (the
// service decides the check-in; this plays its reply), and the script checks what the app sent.
// Usage: node scripts/verify/checkin.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const DAY = "2026-10-20";
const TEXT = "You got the post office done, and lunch with Sam sounded nice. How did the rest of today go?";
const task = (id, title, extra = {}) => ({ id, title, notes: "", list: "Inbox", due: null, rrule: null, priority: 0, completed_at: null, created_at: "", updated_at: "", ...extra });
const cards = [
  { kind: "tasks", id: "tasks:open", title: "Still open", data: { items: [task("3", "Pay rent", { due: "2026-10-19" })] } },
  { kind: "tasks", id: "tasks:done", title: "Done today", data: { items: [task("2", "Post office", { completed_at: "2026-10-20T16:00:00-05:00" }), task("1", "Dishes", { rrule: "FREQ=DAILY", completed_at: "2026-10-20T18:00:00-05:00" })], record: true } },
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
  let socket;
  let turns = 0;
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    socket = ws;
    ws.send(JSON.stringify({ type: "ready", client_id: "c1", conversation_id: "k", capabilities: { stt: true, tts: false }, gestures: ["waving"], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }));
    ws.onMessage((raw) => {
      if (typeof raw !== "string") return;
      const m = JSON.parse(raw);
      sent.push(m);
      if (m.type !== "checkin") return;
      const id = `tc${++turns}`;
      ws.send(JSON.stringify({ type: "turn_start", turn_id: id, input_id: m.id, origin: "c1", user_text: "" }));
      ws.send(JSON.stringify({ type: "activity", turn_id: id, tool: "evening_checkin", label: "Looking back over your day", state: "start" }));
      ws.send(JSON.stringify({ type: "activity", turn_id: id, tool: "evening_checkin", label: "Looking back over your day", state: "done" }));
      for (const card of cards) ws.send(JSON.stringify({ type: "card", turn_id: id, card }));
      ws.send(JSON.stringify({ type: "segment", turn_id: id, seq: 0, text: TEXT, duration: 5, audio_b64: null, audio_format: null, words: null, gestures: [], expressions: [] }));
      ws.send(JSON.stringify({ type: "turn_end", turn_id: id, status: "done", text: TEXT, model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } }));
    });
  });
  await page.route("**/api/**", (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/routines") return json(route, { heartbeat: true, waking_start: "08:00", waking_end: "22:00", checkin: true, checkin_at: "00:00" });
    if (p === "/api/tasks") return json(route, { tasks: [] });
    if (p === "/api/reminders") return json(route, { reminders: [] });
    if (p === "/api/calendar/status") return json(route, { configured: false });
    if (p === "/api/approvals") return json(route, { approvals: [] });
    if (p === "/api/approvals/rules") return json(route, { rules: [] });
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    return route.fulfill({ status: 204 });
  });

  // Opened by the notification.
  await page.goto(`${BASE}?checkin=${DAY}`);
  await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
  await page.getByRole("button", { name: "Open Done today in the menu" }).waitFor({ timeout: 10_000 });
  const asked = sent.filter((m) => m.type === "checkin");
  check(asked.length === 1 && asked[0].day === DAY, `${vname}: the notification's day was sent once (${JSON.stringify(asked)})`);
  check(!new URL(page.url()).searchParams.has("checkin"), `${vname}: the address no longer starts a check-in on reload`);
  check((await page.getByRole("checkbox", { name: /Post office|Dishes/ }).count()) === 0, `${vname}: what was done shows without controls`);
  await page.waitForTimeout(1200);
  await page.screenshot({ path: path.join(out, `checkin-${vname}-opened.png`) });

  // While the app is open: the notice and its button.
  socket.send(JSON.stringify({ type: "notify", id: `checkin-${DAY}`, kind: "checkin", title: "How did today go?", body: "" }));
  const talk = page.getByRole("button", { name: "Talk it over" });
  await talk.waitFor({ timeout: 5000 });
  await page.waitForTimeout(500); // it fades in
  await page.screenshot({ path: path.join(out, `checkin-${vname}-notice.png`) });
  await talk.click();
  await page.waitForTimeout(500);
  const again = sent.filter((m) => m.type === "checkin");
  check(again.length === 2 && again[1].day === undefined && !again[1].quiet, `${vname}: the notice's button asked for tonight's, aloud (${JSON.stringify(again[1])})`);

  await page.goto(BASE + "?menu=settings");
  await page.getByText("Evening check-in").scrollIntoViewIfNeeded();
  await page.waitForTimeout(400);
  const at = page.getByLabel("At", { exact: true });
  check(await page.getByRole("switch", { name: /Evening check-in/ }).isChecked() && (await at.inputValue()) === "00:00", `${vname}: settings show the check-in on, at midnight`);
  await page.screenshot({ path: path.join(out, `checkin-${vname}-settings.png`) });
  await page.close();
}
await browser.close();
