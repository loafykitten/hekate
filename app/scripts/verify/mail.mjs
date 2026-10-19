// Screenshots of the Mail section, approvals and the mail/approval cards, phone and desktop.
// The service may not serve mail yet, so /api/mail/*, /api/approvals* and the conversation socket
// are answered here with fixture data (example.com senders, owner "Rika"). Fixtures live in this
// script only. Usage: node scripts/verify/mail.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const NOW = Date.now();
const ago = (min) => new Date(NOW - min * 60_000).toISOString();

const msg = (id, over = {}) => ({
  id: String(id),
  account: "personal",
  from: { name: "Shopify", address: "no-reply@example.com" },
  subject: "Your weekly update",
  date: ago(60 * 3),
  folder: "INBOX",
  unread: true,
  category: "newsletter",
  gist: "Product news and a sale",
  can_unsubscribe: true,
  ...over,
});

const MESSAGES = [
  msg(401, { from: { name: "Dr. Okafor's office", address: "frontdesk@example.com" }, subject: "Can we move your appointment to Thursday?", date: ago(25), category: "needs_reply", gist: "Asks to move Tuesday's appointment to Thursday at 2pm" }),
  msg(402, { from: { name: "Mina Park", address: "mina@example.com" }, subject: "Re: weekend plans", date: ago(95), category: "personal", gist: "Suggests the lake on Saturday", account: "work" }),
  msg(403, { from: { name: "Example Bank", address: "alerts@example.com" }, subject: "Your statement is ready", date: ago(60 * 20), category: "important", gist: "October statement available", unread: false }),
  msg(404),
  msg(405, { from: { name: "", address: "orders@example.com" }, subject: "Receipt for order #88213", date: ago(60 * 30), category: "receipt", gist: "Oat milk and two other items, $24.18", unread: false }),
  msg(406, { from: { name: "Trailhead Weekly", address: "news@example.com" }, subject: "10 trails to try before the snow", date: ago(60 * 80), category: "newsletter", gist: "Trail roundup" }),
  msg(407, { from: { name: "CityLibrary", address: "noreply@example.com" }, subject: "Your holds are ready for pickup", date: ago(60 * 100), category: "notification", gist: "Two books waiting at the front desk", unread: false, account: "work" }),
];

const LONG_URL = "https://example.com/account/verify?token=" + "a1b2c3d4e5f6".repeat(14) + "&redirect=%2Fsettings%2Fnotifications";
const BODY = `Hi Rika,\n\nThanks for your note. Tuesday doesn't work for Dr. Okafor after all.\nWould Thursday at 2:00 pm suit you instead?\n\nIf so, confirm here and we'll hold the slot:\n${LONG_URL}\n\n    -- Front desk\n    Okafor Family Clinic\n\nP.S. Please bring your insurance card.`;

const RULES = [
  { id: "4", match: { from: "@example.com", category: null }, action: "archive", folder: null, label: "Archive mail from example.com", hits: 12, last_hit_at: ago(60 * 5), created_at: ago(60 * 400) },
  { id: "5", match: { from: null, category: "promo" }, action: "trash", folder: null, label: "Trash promos", hits: 0, last_hit_at: null, created_at: ago(60 * 20) },
  { id: "6", match: { from: "news@example.com", category: null }, action: "move", folder: "Reading", label: "File Trailhead Weekly under Reading", hits: 1, last_hit_at: ago(60 * 30), created_at: ago(60 * 90) },
];

const approval = (id, over = {}) => ({
  id: String(id),
  kind: "mail_trash",
  title: "Move 14 newsletters from example.com to Trash?",
  detail: "They are all unread and older than a week.",
  scope_label: "mail from example.com",
  status: "pending",
  result: null,
  decided_by: null,
  created_at: ago(8),
  expires_at: ago(-60 * 24),
  decided_at: null,
  ...over,
});

const state = {
  mail: "ok", // ok | off | empty | error
  accounts: "two", // two | broken
  approvals: [approval(5), approval(6, { kind: "mail_rule", title: "Always archive receipts from example.com?", detail: "", scope_label: null })],
  approvalRules: [
    { id: "2", kind: "mail_trash", scope_label: "mail from example.com", uses: 3, created_at: ago(60 * 100) },
    { id: "3", kind: "mail_unsubscribe", scope_label: "Trailhead Weekly", uses: 0, created_at: ago(60 * 30) },
  ],
};

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

async function wireApi(page, sockets) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    const method = route.request().method();
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/mail/status") {
      if (state.mail === "error") return json(route, { error: "mail is having a moment" }, 500);
      if (state.mail === "off") return json(route, { configured: false, accounts: [] });
      const accounts = [
        { name: "personal", address: "rika@example.com", ok: true, error: "", last_sweep_at: ago(4), inbox_count: 212 },
        { name: "work", address: "rika@example.org", ok: state.accounts !== "broken", error: state.accounts === "broken" ? "authentication failed" : "", last_sweep_at: ago(4), inbox_count: 31 },
      ];
      return json(route, { configured: true, accounts });
    }
    if (p === "/api/mail/summary") return json(route, { unread: { needs_reply: 1, important: 0, personal: 1, receipt: 0, notification: 0, newsletter: 12, promo: 4 }, attention: state.mail === "empty" ? [] : [MESSAGES[0], MESSAGES[1]] });
    if (p === "/api/mail/messages") {
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const cat = url.searchParams.get("category");
      let list = state.mail === "empty" ? [] : MESSAGES;
      if (cat) list = list.filter((m) => m.category === cat);
      if (q) list = list.filter((m) => JSON.stringify(m).toLowerCase().includes(q));
      return json(route, { messages: list });
    }
    const one = p.match(/^\/api\/mail\/messages\/(\d+)$/);
    if (one) {
      if (one[1] === "999") return json(route, { error: "message not found" }, 404);
      const message = MESSAGES.find((m) => m.id === one[1]) ?? MESSAGES[0];
      return json(route, { message, body: { text: BODY, truncated: one[1] === "401" } });
    }
    if (/^\/api\/mail\/messages\/\d+\/(archive|trash|read)$/.test(p)) return json(route, MESSAGES[0]);
    if (p === "/api/mail/rules") return json(route, { rules: RULES });
    if (p === "/api/approvals/rules") return json(route, { rules: state.approvalRules });
    if (p === "/api/approvals" && method === "GET") {
      const status = url.searchParams.get("status") ?? "pending";
      return json(route, { approvals: status === "pending" ? state.approvals.filter((a) => a.status === "pending") : state.approvals.filter((a) => a.status !== "pending") });
    }
    const decide = p.match(/^\/api\/approvals\/(\d+)\/decide$/);
    if (decide) {
      const { decision } = JSON.parse(route.request().postData() ?? "{}");
      await new Promise((r) => setTimeout(r, 400));
      const a = state.approvals.find((x) => x.id === decide[1]);
      Object.assign(a, decision === "deny" ? { status: "denied", result: null, decided_by: "owner" } : { status: "done", result: "Moved 14 messages to Trash", decided_by: "owner" }, { decided_at: new Date().toISOString() });
      // Other devices hear about it over the socket.
      for (const ws of sockets) ws.send(JSON.stringify({ type: "approval", approval: a }));
      return json(route, a);
    }
    // Everything else the menu asks for: quiet, empty answers so the shots show only mail.
    if (p === "/api/tasks") return json(route, { tasks: [] });
    if (p === "/api/lists") return json(route, { lists: [] });
    if (p === "/api/reminders") return json(route, { reminders: [] });
    if (p === "/api/calendar/status") return json(route, { configured: false });
    if (p === "/api/weather") return json(route, { error: "no place" }, 404);
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    if (p === "/api/health") return json(route, { status: "ok", version: "dev" });
    return route.fulfill({ status: 204 });
  });
}

const READY = { type: "ready", client_id: "c1", conversation_id: "k1", capabilities: { stt: false, tts: false }, gestures: [], timezone: "UTC" };

async function open(browser, name, viewport, query = "", { prepare } = {}) {
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  const sockets = [];
  page.on("pageerror", (e) => console.log(`${name} pageerror:`, e.message));
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    sockets.push(ws);
    ws.send(JSON.stringify(READY));
  });
  await wireApi(page, sockets);
  await prepare?.(page);
  await page.goto(BASE + (query ? `?${query}` : ""));
  return { page, sockets };
}

async function shot(page, name) {
  await page.waitForTimeout(500);
  const file = path.join(out, `mail-${name}.png`);
  await page.screenshot({ path: file });
  console.log("wrote", file);
}

const phone = { width: 375, height: 812 };
const desktop = { width: 1280, height: 800 };
const browser = await launch();

for (const [vname, viewport] of [["phone", phone], ["desktop", desktop]]) {
  const dim = (n) => `${vname}-${n}`;

  // Mail list, two accounts, one with trouble.
  Object.assign(state, { mail: "ok", accounts: "broken" });
  let { page } = await open(browser, dim("list"), viewport, "menu=mail");
  await page.getByText("Needs your attention").waitFor();
  await shot(page, dim("list"));
  await page.getByRole("button", { name: "Newsletters" }).click();
  await shot(page, dim("list-newsletters"));
  await page.getByLabel("Search mail").fill("okafor");
  await page.waitForTimeout(500);
  await shot(page, dim("search"));
  await page.close();

  // One message.
  state.accounts = "two";
  ({ page } = await open(browser, dim("message"), viewport, "menu=mail"));
  await page.getByText("Needs your attention").waitFor();
  await page.getByRole("button", { name: /Can we move your appointment/ }).first().click();
  await page.getByText("Okafor Family Clinic").waitFor();
  await shot(page, dim("message"));
  await page.getByRole("button", { name: "Mark read" }).focus();
  await page.close();

  // Rules.
  ({ page } = await open(browser, dim("rules"), viewport, "menu=mail"));
  await page.getByText("Needs your attention").waitFor();
  await page.getByRole("button", { name: "Rules" }).click();
  await page.getByText("Trash promos").waitFor();
  await shot(page, dim("rules"));
  await page.getByRole("button", { name: /Delete rule Trash promos/ }).click();
  await shot(page, dim("rules-confirm"));
  await page.close();

  // States.
  for (const s of ["off", "empty", "error"]) {
    state.mail = s;
    ({ page } = await open(browser, dim(s), viewport, "menu=mail"));
    await page.waitForTimeout(1200);
    await shot(page, dim(s));
    await page.close();
  }
  state.mail = "ok";

  // Today with approvals, and the decision in the menu.
  ({ page } = await open(browser, dim("today"), viewport, "menu=today"));
  await page.getByText("Needs your OK").waitFor();
  await shot(page, dim("today"));
  await page.close();

  // Settings.
  ({ page } = await open(browser, dim("settings"), viewport, "menu=settings"));
  await page.getByText("Standing permissions").waitFor();
  await page.getByText("Move mail to Trash").waitFor();
  await page.getByRole("button", { name: /Revoke Move mail to Trash/ }).click();
  await shot(page, dim("settings"));
  await page.close();
}

// Cards on stage, through notify events on the mocked socket.
async function cardShot(name, viewport, card, kind = "reminder") {
  const { page, sockets } = await open(browser, name, viewport);
  await page.waitForFunction(() => document.body.innerText.includes("Hekate") || true);
  await page.waitForTimeout(800);
  for (const ws of sockets) ws.send(JSON.stringify({ type: "notify", id: `n-${name}`, kind, title: card.title, body: "", card }));
  await page.getByRole("article").first().waitFor();
  page.sockets = sockets;
  return page;
}

for (const [vname, viewport] of [["phone", phone], ["desktop", desktop]]) {
  state.approvals = [approval(5), approval(6, { kind: "mail_rule", title: "Always archive receipts from example.com?", detail: "", scope_label: null })];

  let page = await cardShot(`${vname}-card-mail`, viewport, { kind: "mail", id: "mail-1", title: "Mail", data: { items: [MESSAGES[0], MESSAGES[1], MESSAGES[3]], highlight: ["401"] } });
  await shot(page, `${vname}-card-mail`);
  await page.close();

  page = await cardShot(`${vname}-card-message`, viewport, { kind: "mail_message", id: "mm-401", title: "Can we move your appointment to Thursday?", data: { message: MESSAGES[0], excerpt: BODY.slice(0, 220) } });
  await shot(page, `${vname}-card-message`);
  await page.getByRole("button", { name: /Dr. Okafor/ }).click();
  await page.getByText("Okafor Family Clinic").waitFor();
  await shot(page, `${vname}-card-message-opened`);
  await page.close();

  page = await cardShot(`${vname}-card-approval`, viewport, { kind: "approval", id: "approval-5", title: state.approvals[0].title, data: state.approvals[0] });
  await shot(page, `${vname}-card-approval`);
  await page.getByRole("button", { name: "Allow once" }).click();
  await page.waitForTimeout(150);
  await shot(page, `${vname}-card-approval-busy`);
  await page.getByText("Moved 14 messages to Trash").waitFor();
  await shot(page, `${vname}-card-approval-done`);
  await page.close();

  // No scope: no "Always".
  state.approvals = [approval(5), approval(6, { kind: "mail_rule", title: "Always archive receipts from example.com?", detail: "", scope_label: null })];
  page = await cardShot(`${vname}-card-approval-noscope`, viewport, { kind: "approval", id: "approval-6", title: "x", data: state.approvals[1] });
  await shot(page, `${vname}-card-approval-noscope`);
  await page.close();

  // Decided on another device: the socket event flips the open card.
  state.approvals = [approval(5)];
  const r = await cardShot(`${vname}-card-approval-live`, viewport, { kind: "approval", id: "approval-5", title: "x", data: state.approvals[0] });
  for (const ws of r.sockets) ws.send(JSON.stringify({ type: "approval", approval: approval(5, { status: "running" }) }));
  await r.getByText("Working on it…").waitFor();
  await shot(r, `${vname}-card-approval-running`);
  for (const ws of r.sockets) ws.send(JSON.stringify({ type: "approval", approval: approval(5, { status: "failed", result: "The mail server refused the move: mailbox is read-only", decided_by: "owner", decided_at: ago(0) }) }));
  await r.getByText("Didn't work").waitFor();
  await shot(r, `${vname}-card-approval-failed`);
  await r.close();
}

await browser.close();
console.log(`wrote ${out}/`);
