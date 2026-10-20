// Screenshots of the Changes section and the deny-with-reason step on approval cards, phone and
// desktop. /api/changes, /api/approvals* and the conversation socket are answered here with
// fixture data (owner "Rika"). Usage: node scripts/verify/changes.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const NOW = Date.now();
const ago = (min) => new Date(NOW - min * 60_000).toISOString();

const change = (id, over = {}) => ({
  id: String(id),
  title: "Match mail rules on subject words",
  what: "Let a mail rule match words in the subject line, not only the sender.\nShow the matched words in the rule label.",
  why: "Newsletters from many senders share subject words, so one rule could replace five.",
  kind: "feature",
  revises: null,
  status: "queued",
  approval_id: String(40 + id),
  feedback: null,
  stage: "",
  issue_url: null,
  pr_url: null,
  result: null,
  created_at: ago(300),
  updated_at: ago(20),
  finished_at: null,
  ...over,
});

const CHANGES = [
  change(1, { status: "proposed" }),
  change(2, { title: "Read the weather before suggesting a walk", kind: "improvement", status: "working", stage: "PR open, in review", issue_url: "https://github.com/example/hekate/issues/12", pr_url: "https://github.com/example/hekate/pull/13" }),
  change(3, { title: "Snooze reminders by voice", status: "merged", result: "Merged and deployed. Say \"snooze that for an hour\" to try it.", finished_at: ago(90), pr_url: "https://github.com/example/hekate/pull/9" }),
  change(4, { title: "Rewrite the whole scheduler", kind: "improvement", status: "denied", feedback: "Too big for one change. Try the retry logic alone.", finished_at: ago(600) }),
  change(5, { title: "Fix duplicate calendar events", kind: "fix", status: "failed", stage: "Building", result: "The build failed: tests for recurring events broke.", finished_at: ago(2000), attempts: 2, previous_result: "Main moved before it could merge.", previous_pr_url: "https://github.com/example/hekate/pull/7" }),
  change(6, { title: "Add a haiku mode", status: "expired" }),
];

const PRISTINE = structuredClone(CHANGES); // each viewport starts from these

const LONG = "Build: Match mail rules on subject words?";
const DETAIL = `What: let a mail rule match words in the subject line, not only the sender. The rule label would show the matched words so you can see why a message was filed.\nWhy: newsletters from many senders share subject words, so one rule could replace five.\nKind: feature\nTouches: mail rule matching, the rules list in Mail.\nRisk: low, rules are only applied to new mail.`;

const approval = (id, over = {}) => ({
  id: String(id), kind: "change_request", title: LONG, detail: DETAIL, scope_label: null, status: "pending",
  result: null, decided_by: null, note: null, created_at: ago(8), expires_at: ago(-60 * 24 * 7), decided_at: null, ...over,
});

const state = { approvals: [approval(41)], decisions: [], retried: [] };
const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

async function wireApi(page, sockets) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/changes") return json(route, { changes: CHANGES });
    const retry = p.match(/^\/api\/changes\/(\d+)\/retry$/);
    if (retry && method === "POST") {
      const c = CHANGES.find((x) => x.id === retry[1]);
      Object.assign(c, { status: "queued", previous_result: c.result, previous_pr_url: c.pr_url ?? c.previous_pr_url, result: null, pr_url: null, finished_at: null, stage: "" });
      state.retried.push(c.id);
      return json(route, c);
    }
    if (p === "/api/ideas") return json(route, { ideas: [
      { id: "2", text: "A tool to read parcel tracking pages, so I can say when a delivery arrives.", kind: "tool", status: "open", created_at: ago(200), updated_at: ago(200) },
      { id: "1", text: "Weather lookups time out now and then in the morning; retry once before giving up.", kind: "fix", status: "open", created_at: ago(3000), updated_at: ago(3000) },
    ] });
    if (p === "/api/approvals/rules") return json(route, { rules: [] });
    if (p === "/api/approvals" && method === "GET") {
      const pending = new URL(route.request().url()).searchParams.get("status") !== "recent";
      return json(route, { approvals: state.approvals.filter((a) => (a.status === "pending") === pending) });
    }
    const decide = p.match(/^\/api\/approvals\/(\d+)\/decide$/);
    if (decide && method === "POST") {
      const body = JSON.parse(route.request().postData() ?? "{}");
      state.decisions.push(body);
      const a = state.approvals.find((x) => x.id === decide[1]);
      Object.assign(a, { status: "denied", decided_by: "owner", note: body.note ?? null, decided_at: ago(0) });
      for (const ws of sockets) ws.send(JSON.stringify({ type: "approval", approval: a }));
      return json(route, a);
    }
    if (p === "/api/tasks") return json(route, { tasks: [] });
    if (p === "/api/lists") return json(route, { lists: [] });
    if (p === "/api/reminders") return json(route, { reminders: [] });
    if (p === "/api/calendar/status") return json(route, { configured: false });
    if (p === "/api/weather") return json(route, { error: "no place" }, 404);
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    if (p === "/api/mail/status") return json(route, { configured: false, accounts: [] });
    if (p === "/api/mail/summary") return json(route, { unread: {}, attention: [] });
    if (p === "/api/health") return json(route, { status: "ok", version: "dev" });
    return route.fulfill({ status: 204 });
  });
}

const READY = { type: "ready", client_id: "c1", conversation_id: "k1", capabilities: { stt: false, tts: false }, gestures: [], timezone: "UTC" };

async function open(browser, name, viewport, query = "") {
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  const sockets = [];
  page.on("pageerror", (e) => console.log(`${name} pageerror:`, e.message));
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    sockets.push(ws);
    ws.send(JSON.stringify(READY));
  });
  await wireApi(page, sockets);
  await page.goto(BASE + (query ? `?${query}` : ""));
  return { page, sockets };
}

async function shot(page, name) {
  await page.waitForTimeout(500);
  const file = path.join(out, `changes-${name}.png`);
  await page.screenshot({ path: file });
  console.log("wrote", file);
}

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 375, height: 812 }], ["desktop", { width: 1280, height: 800 }]]) {
  CHANGES.splice(0, CHANGES.length, ...structuredClone(PRISTINE));
  let { page } = await open(browser, `${vname}-list`, viewport, "menu=changes");
  await page.getByText("Snooze reminders by voice").waitFor();
  await shot(page, `${vname}-list`);
  await page.getByText("Her ideas").scrollIntoViewIfNeeded();
  await shot(page, `${vname}-ideas`);
  await page.getByRole("button", { name: /Match mail rules/ }).click();
  await shot(page, `${vname}-list-open`);
  // A failed change: its last attempt, then Try again sends it back to the queue.
  await page.getByRole("button", { name: /^Fix duplicate calendar events/ }).click();
  await shot(page, `${vname}-failed-open`);
  await page.getByRole("button", { name: "Try “Fix duplicate calendar events” again" }).click();
  await page.getByText("Back on the list").waitFor();
  await shot(page, `${vname}-retried`);
  console.log("retried", state.retried);
  await page.close();

  // Deny with a reason on the approval card.
  state.approvals = [approval(41)];
  state.decisions = [];
  let sockets;
  ({ page, sockets } = await open(browser, `${vname}-card`, viewport));
  await page.waitForTimeout(800);
  const card = { kind: "approval", id: "approval-41", title: LONG, data: state.approvals[0] };
  for (const ws of sockets) ws.send(JSON.stringify({ type: "notify", id: `n-${vname}`, kind: "approval", title: "Needs your OK", body: LONG, card }));
  await page.getByRole("article").first().waitFor();
  await shot(page, `${vname}-card`);
  await page.getByRole("button", { name: "More" }).click();
  await shot(page, `${vname}-card-more`);
  await page.getByRole("button", { name: "Deny" }).click();
  await page.getByLabel(/Why not/).fill("Too broad. Try the retry logic on its own first.");
  await shot(page, `${vname}-card-deny`);
  await page.getByRole("button", { name: "Deny" }).click();
  await page.getByText("You said no").waitFor();
  await shot(page, `${vname}-card-denied`);
  console.log("posted", JSON.stringify(state.decisions));
  await page.close();
}
await browser.close();
