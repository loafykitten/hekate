// The Money section and its cards, phone and desktop, with the service answered by fixtures
// (Example Bank, made-up merchants). Checks that Plaid's script isn't loaded just by opening the
// section, that connecting posts what Link returns, and that the return from a bank's own site
// (/money/oauth) resumes Link with the stored token. Screenshots: money-<viewport>-<scene>.png.
// Usage: node scripts/verify/money.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const now = new Date();
const month = `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, "0")}`;
const day = (back) => {
  const d = new Date(now.getTime() - back * 86_400_000);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
};
const syncedAt = new Date(now.getTime() - 25 * 60_000).toISOString();

const items = [
  { id: "1", institution: "Example Bank", status: "ok", error: "", last_sync_at: syncedAt, accounts: 3, history: "partial" },
  { id: "2", institution: "Example Credit Union", status: "login_required", error: "ITEM_LOGIN_REQUIRED", last_sync_at: new Date(now.getTime() - 3 * 86_400_000).toISOString(), accounts: 1 },
];
const acct = (id, item_id, institution, name, mask, type, subtype, balance) => ({ id, item_id, institution, name, mask, type, subtype, balance, currency: "USD", updated_at: syncedAt });
const accounts = [
  acct("acc_1", "1", "Example Bank", "Everyday Checking", "1234", "depository", "checking", { current: 1520.31, available: 1490.31, limit: null }),
  acct("acc_2", "1", "Example Bank", "Rainy Day Savings", "5678", "depository", "savings", { current: 8200, available: 8200, limit: null }),
  acct("acc_3", "1", "Example Bank", "Rewards Card", "9012", "credit", "credit card", { current: 412.88, available: 4587.12, limit: 5000 }),
  acct("acc_4", "2", "Example Credit Union", "Share Account", "3456", "depository", "savings", { current: 640, available: null, limit: null }),
];
const txn = (id, back, merchant, name, amount, category, label, extra = {}) => ({
  id, account_id: "acc_1", account: "Everyday Checking", date: day(back), name, merchant, amount, currency: "USD", pending: false, category, category_label: label, detail: null, ...extra,
});
const transactions = [
  txn("t1", 0, "Corner Cafe", "SQ *CORNER CAFE", -6.75, "FOOD_AND_DRINK", "Food and drink", { pending: true }),
  txn("t2", 1, "Greenleaf Market", "GREENLEAF MARKET #12", -84.2, "FOOD_AND_DRINK", "Food and drink"),
  txn("t3", 2, "Paper Moon Books", "PAPER MOON BOOKS", -23.5, "ENTERTAINMENT", "Entertainment"),
  txn("t4", 3, null, "ACME PAYROLL DEPOSIT", 2400, "INCOME", "Income"),
  txn("t5", 4, "Northline Transit", "NORTHLINE TRANSIT PASS", -45, "TRANSPORTATION", "Transportation"),
  txn("t6", 5, "Lakeview Apartments With A Rather Long Name For Rent", "LAKEVIEW APTS RENT", -1250, "RENT_AND_UTILITIES", "Rent and utilities"),
  txn("t7", 6, "Moonlit Streaming", "MOONLIT STREAMING", -11.99, "ENTERTAINMENT", "Entertainment", { account_id: "acc_3", account: "Rewards Card" }),
];
const spending = {
  month,
  currency: "USD",
  spent: 1421.44,
  received: 2400,
  categories: [
    { category: "RENT_AND_UTILITIES", label: "Rent and utilities", spent: 1250, count: 1, previous: 1250 },
    { category: "FOOD_AND_DRINK", label: "Food and drink", spent: 312.4, count: 23, previous: 380.1 },
    { category: "TRANSPORTATION", label: "Transportation", spent: 45, count: 2, previous: 90 },
    { category: "ENTERTAINMENT", label: "Entertainment", spent: 35.49, count: 2, previous: 61 },
  ],
  previous_spent: 1781.1,
  transfers: 500,
};
const cards = [
  { kind: "accounts", id: "c:acc", title: "Accounts", data: { items: accounts } },
  { kind: "transactions", id: "c:txn", title: "Recent transactions", data: { items: transactions.slice(0, 5), highlight: ["t1"] } },
  { kind: "spending", id: "c:spend", title: "Spending this month", data: spending },
];

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const PLAID_STUB = `window.Plaid = { create(c) { window.__plaid = c; return { open() { window.__plaidOpened = (window.__plaidOpened || 0) + 1; if (window.__plaidAuto) setTimeout(() => c.onSuccess("public-sandbox-x", { institution: { institution_id: "ins_example", name: "Example Bank" } }), 30); }, destroy() {} }; } };`;

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 390, height: 844 }], ["desktop", { width: 1280, height: 800 }]]) {
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  page.on("pageerror", (e) => console.log(`${vname} pageerror:`, e.message));
  let scene = "data"; // data | unconfigured | empty
  const posted = [];
  const plaidRequests = [];
  let socket;
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    socket = ws;
    ws.send(JSON.stringify({ type: "ready", client_id: "c1", conversation_id: "k", capabilities: { stt: true, tts: false }, gestures: ["waving"], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone }));
  });
  await page.route("**/cdn.plaid.com/**", (route) => {
    plaidRequests.push(route.request().url());
    return route.fulfill({ status: 200, contentType: "text/javascript", body: PLAID_STUB });
  });
  await page.route("**/api/**", (route) => {
    const req = route.request();
    const u = new URL(req.url());
    const p = u.pathname;
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/money/status") return json(route, scene === "unconfigured" ? { configured: false, items: [] } : { configured: true, items: scene === "empty" ? [] : items });
    if (p === "/api/money/accounts") return json(route, { accounts: scene === "empty" ? [] : accounts });
    if (p === "/api/money/spending") return json(route, { ...spending, month: u.searchParams.get("month") ?? month });
    if (p === "/api/money/transactions") {
      const q = (u.searchParams.get("q") ?? "").toLowerCase();
      const cat = u.searchParams.get("category") ?? "";
      return json(route, { transactions: transactions.filter((t) => (!q || `${t.name} ${t.merchant}`.toLowerCase().includes(q)) && (!cat || t.category === cat)) });
    }
    if (p === "/api/money/link-token") {
      posted.push({ path: p, body: req.postDataJSON() });
      return json(route, { link_token: "link-sandbox-abc" });
    }
    if (p === "/api/money/items" && req.method() === "POST") {
      posted.push({ path: p, body: req.postDataJSON() });
      return json(route, items[0], 201);
    }
    if (p === "/api/money/sync") {
      posted.push({ path: p });
      return route.fulfill({ status: 202, contentType: "application/json", body: "{}" });
    }
    if (p === "/api/routines") return json(route, { heartbeat: true, waking_start: "08:00", waking_end: "22:00", checkin: true, checkin_at: "00:00" });
    if (p === "/api/tasks") return json(route, { tasks: [] });
    if (p === "/api/reminders") return json(route, { reminders: [] });
    if (p === "/api/calendar/status") return json(route, { configured: false });
    if (p === "/api/approvals") return json(route, { approvals: [] });
    if (p === "/api/approvals/rules") return json(route, { rules: [] });
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    return route.fulfill({ status: 204 });
  });
  const shot = (name) => page.screenshot({ path: path.join(out, `money-${vname}-${name}.png`) });
  const open = async (s) => {
    scene = s;
    await page.goto(`${BASE}?menu=money`);
    await page.getByRole("dialog", { name: "Menu" }).waitFor({ timeout: 30_000 });
    await page.waitForTimeout(700);
  };

  // Configured, with data. Opening the section must not pull Plaid in.
  await open("data");
  await page.getByText("Recent transactions").waitFor({ timeout: 10_000 });
  await page.getByText("Greenleaf Market").waitFor();
  check(plaidRequests.length === 0 && (await page.evaluate(() => window.Plaid === undefined)), `${vname}: opening Money doesn't load Plaid`);
  check((await page.getByRole("button", { name: "Reconnect" }).count()) === 1, `${vname}: only the bank that needs it offers Reconnect`);
  check((await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)), `${vname}: no sideways page scroll`);
  await page.waitForTimeout(300);
  await shot("data");
  await page.getByRole("dialog", { name: "Menu" }).locator("nav").screenshot({ path: path.join(out, `money-${vname}-nav.png`) });
  const content = page.locator("[class*=content]").last();
  await page.getByRole("heading", { name: "Spending" }).evaluate((el) => el.scrollIntoView({ block: "start" }));
  await page.waitForTimeout(300);
  await shot("spending");
  await content.evaluate((el) => el.scrollTo({ top: el.scrollHeight }));
  await page.waitForTimeout(300);
  await shot("data-bottom");

  // Search and category filter go to the service.
  await page.getByLabel("Search transactions").fill("books");
  await page.waitForTimeout(700);
  check((await page.getByText("Paper Moon Books").count()) === 1 && (await page.getByText("Greenleaf Market").count()) === 0, `${vname}: search narrows the list`);
  await page.getByLabel("Search transactions").fill("");
  await page.getByRole("button", { name: "Entertainment", exact: true }).click();
  await page.waitForTimeout(500);
  check((await page.getByText("Moonlit Streaming").count()) === 1 && (await page.getByText("Corner Cafe").count()) === 0, `${vname}: category chip filters`);
  await page.getByRole("button", { name: "All", exact: true }).click();

  // Month switcher: previous month enabled, next disabled this month.
  await content.evaluate((el) => el.scrollTo({ top: 0 }));
  check(await page.getByRole("button", { name: "Next month" }).isDisabled(), `${vname}: can't go past this month`);
  await page.getByRole("button", { name: "Previous month" }).click();
  await page.waitForTimeout(400);
  check(await page.getByRole("button", { name: "Next month" }).isEnabled(), `${vname}: can come back`);

  // Reconnect posts a link token for that item, opens Link, and on success only syncs.
  await page.evaluate(() => (window.__plaidAuto = true));
  posted.length = 0;
  await page.getByRole("button", { name: "Reconnect" }).click();
  await page.waitForFunction(() => window.__plaidOpened === 1, null, { timeout: 5000 });
  await page.waitForTimeout(500);
  check(plaidRequests.length === 1, `${vname}: Plaid's script loaded once, on demand`);
  check(posted[0]?.body?.item_id === "2" && posted.some((m) => m.path === "/api/money/sync") && !posted.some((m) => m.path === "/api/money/items"), `${vname}: reconnect asks for an update-mode token and only syncs (${JSON.stringify(posted)})`);
  await shot("reconnected");

  // Disconnect asks twice.
  const disconnect = page.getByRole("button", { name: "Disconnect Example Bank" });
  await disconnect.click();
  check(await page.getByRole("button", { name: /Confirm disconnect: Example Bank/ }).isVisible(), `${vname}: disconnect needs a second tap`);
  await shot("disconnect-confirm");

  // Not configured.
  await open("unconfigured");
  await page.getByText("Bank data isn't set up yet").waitFor();
  await shot("unconfigured");

  // Configured, no banks: connect posts what Link returns.
  await open("empty");
  await page.getByRole("button", { name: "Connect a bank" }).waitFor();
  await shot("empty");
  posted.length = 0;
  await page.evaluate(() => (window.__plaidAuto = true));
  await page.getByRole("button", { name: "Connect a bank" }).click();
  await page.waitForTimeout(1200);
  const added = posted.find((m) => m.path === "/api/money/items");
  check(added?.body?.public_token === "public-sandbox-x" && added.body.institution?.name === "Example Bank" && added.body.institution?.id === "ins_example", `${vname}: connecting posts the public token and institution (${JSON.stringify(added)})`);
  await shot("connected");

  // Back from a bank's own site.
  scene = "data";
  posted.length = 0;
  await page.addInitScript(() => {
    window.__plaidAuto = true;
    if (location.pathname === "/money/oauth") localStorage.setItem("hekate-plaid-link", JSON.stringify({ token: "link-sandbox-oauth", at: Date.now() }));
  });
  await page.goto(`${BASE}money/oauth?oauth_state_id=abc123`);
  await page.getByRole("dialog", { name: "Menu" }).waitFor({ timeout: 30_000 });
  await page.waitForFunction(() => window.__plaid, null, { timeout: 5000 });
  const cfg = await page.evaluate(() => ({ token: window.__plaid.token, uri: window.__plaid.receivedRedirectUri }));
  check(cfg.token === "link-sandbox-oauth" && /\/money\/oauth\?oauth_state_id=abc123$/.test(cfg.uri), `${vname}: OAuth return resumes Link with the stored token (${JSON.stringify(cfg)})`);
  await page.waitForTimeout(800);
  check(new URL(page.url()).pathname === "/" && new URL(page.url()).searchParams.get("menu") === "money", `${vname}: address goes back to the Money section (${page.url()})`);
  // The stubbed Link finishes on a timer: wait for its POST rather than a fixed time.
  for (let i = 0; i < 50 && !posted.some((m) => m.path === "/api/money/items"); i++) await page.waitForTimeout(100);
  check(posted.some((m) => m.path === "/api/money/items"), `${vname}: the resumed Link finished the connection`);
  await page.addInitScript(() => localStorage.removeItem("hekate-plaid-link"));

  // Nothing stored: gentle note.
  await page.evaluate(() => localStorage.removeItem("hekate-plaid-link"));
  await page.evaluate(() => history.pushState({}, "", "/money/oauth?oauth_state_id=zzz"));
  await page.reload();
  await page.getByText(/didn't carry over/).waitFor({ timeout: 15_000 });
  await shot("oauth-lost");

  // Cards.
  scene = "data";
  await page.goto(BASE);
  await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
  await page.waitForTimeout(500);
  const id = "tm1";
  socket.send(JSON.stringify({ type: "turn_start", turn_id: id, input_id: "in1", origin: "c1", user_text: "How am I doing on money?" }));
  for (const card of cards) socket.send(JSON.stringify({ type: "card", turn_id: id, card }));
  socket.send(JSON.stringify({ type: "segment", turn_id: id, seq: 0, text: "Here is how things look this month.", duration: 3, audio_b64: null, audio_format: null, words: null, gestures: [], expressions: [] }));
  socket.send(JSON.stringify({ type: "turn_end", turn_id: id, status: "done", text: "Here is how things look this month.", model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } }));
  await page.getByRole("button", { name: "Open Spending this month in the menu" }).waitFor({ timeout: 10_000 });
  await page.waitForTimeout(900);
  const titles = ["Accounts", "Recent transactions", "Spending this month"];
  for (let i = 0; i < titles.length; i++) {
    // The stack shows one card at a time; dismiss the top to reach the next.
    const visible = page.getByRole("button", { name: /^Open .* in the menu$/ }).first();
    await visible.waitFor();
    const label = await visible.getAttribute("aria-label");
    await page.waitForTimeout(400);
    await shot(`card-${label.replace(/^Open | in the menu$/g, "").toLowerCase().replace(/\W+/g, "-")}`);
    await page.getByRole("button", { name: /^Dismiss / }).first().click();
    await page.waitForTimeout(500);
  }
  await page.close();
}
await browser.close();
