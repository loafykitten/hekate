// A tour of every screen, phone and desktop, for UX review: the stage and dock, a conversation
// turn, notices and approvals, the log, each menu section with realistic data, empty states and
// the service-unreachable state. REST and the socket are answered here with fictional fixtures
// (owner "Rika", example.com addresses); times are relative to now.
// Usage: node scripts/verify/tour.mjs [outDir]   (files: tour-<viewport>-<screen>.png)
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

// ---- fixture helpers ------------------------------------------------------------------------

const NOW = Date.now();
const MIN = 60_000;
const iso = (ms) => new Date(ms).toISOString();
const ago = (min) => iso(NOW - min * MIN);
const ahead = (min) => iso(NOW + min * MIN);
const pad = (n) => String(n).padStart(2, "0");
function dateKey(offsetDays) {
  const d = new Date(NOW);
  d.setDate(d.getDate() + offsetDays);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
function atDay(offsetDays, h, m = 0) {
  const d = new Date(NOW);
  d.setDate(d.getDate() + offsetDays);
  d.setHours(h, m, 0, 0);
  return d.toISOString();
}

const LONG_TITLE = "Finish and send the quarterly neighbourhood association budget summary, including the revised playground repair estimate and the notes from the March meeting";

const task = (id, title, over = {}) => ({ id: String(id), title, notes: "", list: "Inbox", due: null, rrule: null, priority: 0, completed_at: null, created_at: ago(5000), updated_at: ago(60), ...over });
const TASKS = [
  task(1, "Pay rent", { due: dateKey(-2), priority: 3, list: "Home", notes: "Portal login is in the password manager" }),
  task(2, "Reply to the landlord about the lease", { due: dateKey(-1), priority: 2, list: "Home" }),
  task(3, "Book dentist follow-up", { due: dateKey(0), priority: 2, list: "Health" }),
  task(4, "Water the plants", { due: dateKey(0), rrule: "FREQ=WEEKLY;BYDAY=TU,SA", list: "Home" }),
  task(5, LONG_TITLE, { due: dateKey(0), priority: 1, list: "Work", notes: "Numbers are in the shared sheet" }),
  task(6, "Oat milk", { list: "Groceries" }),
  task(7, "Sourdough starter", { list: "Groceries", due: dateKey(1) }),
  task(8, "Eggs", { list: "Groceries" }),
  task(9, "Call Mina about Saturday", { due: atDay(1, 18, 30), priority: 1, list: "Inbox" }),
  task(10, "Renew library card", { due: dateKey(5), list: "Errands" }),
  task(11, "Take out recycling", { rrule: "FREQ=WEEKLY;BYDAY=WE", due: dateKey(2), list: "Home" }),
  task(12, "Order printer ink", { list: "Work", completed_at: ago(300) }),
  task(13, "Send photos to Grandma", { list: "Inbox", completed_at: ago(1500) }),
];
const LISTS = [
  { name: "Inbox", open_count: 2 },
  { name: "Home", open_count: 4 },
  { name: "Groceries", open_count: 3 },
  { name: "Work", open_count: 1 },
  { name: "Health", open_count: 1 },
  { name: "Errands", open_count: 1 },
];

const reminder = (id, text, at, over = {}) => ({ id: String(id), text, at, rrule: null, status: "scheduled", last_fired_at: null, created_at: ago(3000), ...over });
const REMINDERS = [
  reminder(1, "Leave for the dentist", ahead(95)),
  reminder(2, "Take vitamins", atDay(1, 8, 0), { rrule: "FREQ=DAILY" }),
  reminder(3, "Call Mom", atDay(1, 19, 0)),
  reminder(4, "Put the bins out", atDay(2, 7, 30), { rrule: "FREQ=WEEKLY;BYDAY=WE" }),
  reminder(5, "Renew the car registration before the end of the month, bring proof of insurance", atDay(6, 10, 0)),
  reminder(6, "Stretch break", ago(180), { status: "done", last_fired_at: ago(180) }),
  reminder(7, "Submit timesheet", ago(1500), { status: "fired", last_fired_at: ago(1500) }),
];

const event = (uid, title, start, end, over = {}) => ({ uid, instance: null, title, start, end, all_day: false, location: "", notes: "", rrule: null, created_by_hekate: false, version: "v1", ...over });
const EVENTS = [
  event("e1", "Stand-up", atDay(0, 9, 30), atDay(0, 9, 45), { rrule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", instance: atDay(0, 9, 30) }),
  event("e2", "Dentist: Dr. Okafor", atDay(0, 15, 0), atDay(0, 16, 0), { location: "Okafor Family Clinic, 14 Elm Street", created_by_hekate: true }),
  event("e3", "Dinner with Mina", atDay(0, 19, 0), atDay(0, 21, 0), { location: "Juniper" }),
  event("e4", "Design review for the autumn product launch and the accessibility audit follow-ups", atDay(1, 11, 0), atDay(1, 12, 30), { location: "Room 4B" }),
  event("e5", "Rika's day off", dateKey(2), dateKey(3), { all_day: true }),
  event("e6", "Yoga", atDay(2, 18, 0), atDay(2, 19, 0), { rrule: "FREQ=WEEKLY;BYDAY=TH", instance: atDay(2, 18, 0) }),
  event("e7", "Lake day with Mina", atDay(4, 10, 0), atDay(4, 16, 0), { location: "Pine Lake" }),
  event("e8", "Trip to Portland", dateKey(5), dateKey(7), { all_day: true }),
  event("e9", "Pick up Grandma's package", atDay(5, 13, 15), atDay(5, 13, 45)),
];

const mem = (id, kind, content, over = {}) => ({ id: String(id), kind, content, importance: 3, follow_up_at: null, created_at: ago(9000), updated_at: ago(900), ...over });
const MEMORIES = [
  mem(1, "preference", "Takes coffee black, but likes oat milk in tea", { importance: 4 }),
  mem(2, "person", "Mina Park is a close friend, they hike together most weekends", { importance: 5 }),
  mem(3, "fact", "Works as a product designer; the team stand-up is at 9:30 on weekdays"),
  mem(4, "follow_up", "Ask how the interview on Thursday went", { follow_up_at: ahead(60 * 40), importance: 4 }),
  mem(5, "event", "Birthday on March 3rd; prefers a small dinner over a party"),
  mem(6, "fact", "Allergic to shellfish. Mentioned it twice, so check restaurant suggestions", { importance: 5 }),
  mem(7, "preference", "Likes short answers in the morning and longer ones in the evening", { importance: 2 }),
  mem(8, "person", "Dr. Okafor is the dentist; the office is on Elm Street", { importance: 2 }),
];
const CORE = [
  { key: "name", value: "Rika", updated_at: ago(20000) },
  { key: "home", value: "A small apartment, second floor, with a balcony of plants", updated_at: ago(12000) },
  { key: "routine", value: "Up around 7, works from home Tuesdays and Thursdays, in bed by 11", updated_at: ago(3000) },
];

const JOURNAL = [
  {
    day: dateKey(-1),
    diary: { day: dateKey(-1), text: "Rika's interview moved to Thursday. They sounded tired this morning but steadier by evening, and laughed about the oolong. Mina visits Saturday; I want to remember to ask how it went.\n\nA good day, mostly. I like being the steady one.", created_at: ago(500), updated_at: ago(500) },
    reflections: [
      {
        id: "3",
        summary: "Saved 1, updated 1 and retired 1 memories",
        created_at: ago(500),
        changes: [
          { id: "11", action: "update", memory_id: "4", before: mem(4, "fact", "Has a job interview on Friday"), after: MEMORIES[3], reason: "The interview moved", undone_at: null },
          { id: "12", action: "retire", memory_id: "9", before: mem(9, "preference", "Likes oolong"), after: null, reason: "Merged into a fuller memory", undone_at: null },
          { id: "13", action: "save", memory_id: "51", before: null, after: mem(51, "follow_up", "Ask how Mina's visit went"), reason: "Mina visits Saturday", undone_at: null },
        ],
      },
    ],
  },
  { day: dateKey(-2), diary: { day: dateKey(-2), text: "Quiet Sunday. We sorted the groceries and talked about nothing much. Nice.", created_at: ago(2000), updated_at: ago(2000) }, reflections: [] },
];

const msg = (id, over = {}) => ({ id: String(id), account: "personal", from: { name: "Trailhead Weekly", address: "news@example.com" }, subject: "10 trails to try before the snow", date: ago(180), folder: "INBOX", unread: true, category: "newsletter", gist: "Trail roundup", can_unsubscribe: true, ...over });
const MAIL = [
  msg(401, { from: { name: "Dr. Okafor's office", address: "frontdesk@example.com" }, subject: "Can we move your appointment to Thursday?", date: ago(25), category: "needs_reply", gist: "Asks to move Tuesday's appointment to Thursday at 2pm", can_unsubscribe: false }),
  msg(402, { from: { name: "Mina Park", address: "mina@example.com" }, subject: "Re: weekend plans", date: ago(95), category: "personal", gist: "Suggests the lake on Saturday", account: "work", can_unsubscribe: false }),
  msg(403, { from: { name: "Example Bank", address: "alerts@example.com" }, subject: "Your statement is ready", date: ago(1200), category: "important", gist: "October statement available", unread: false, can_unsubscribe: false }),
  msg(404, { from: { name: "Shopfront", address: "no-reply@example.com" }, subject: "Your weekly update: new arrivals, a sale on autumn knitwear, and three more things we think you will like", date: ago(1500), gist: "Product news and a sale" }),
  msg(405, { from: { name: "", address: "orders@example.com" }, subject: "Receipt for order #88213", date: ago(1800), category: "receipt", gist: "Oat milk and two other items, $24.18", unread: false, can_unsubscribe: false }),
  msg(406),
  msg(407, { from: { name: "CityLibrary", address: "noreply@example.com" }, subject: "Your holds are ready for pickup", date: ago(6000), category: "notification", gist: "Two books waiting at the front desk", unread: false, account: "work", can_unsubscribe: false }),
];
const MAIL_BODY = "Hi Rika,\n\nThanks for your note. Tuesday doesn't work for Dr. Okafor after all.\nWould Thursday at 2:00 pm suit you instead?\n\nIf so, confirm here and we'll hold the slot:\nhttps://example.com/confirm/abc123\n\n-- Front desk\nOkafor Family Clinic\n\nP.S. Please bring your insurance card.";
const MAIL_RULES = [
  { id: "4", match: { from: "@example.com", category: null, subject: null, body: null }, except: [{ category: "receipt" }], action: "archive", folder: null, label: "Archive mail from example.com", hits: 12, last_hit_at: ago(300), created_at: ago(20000) },
  { id: "6", match: { from: "news@example.com", category: null, subject: null, body: null }, except: [], action: "move", folder: "Reading", label: "File Trailhead Weekly under Reading", hits: 1, last_hit_at: ago(1800), created_at: ago(5000) },
];

const approval = (id, over = {}) => ({ id: String(id), kind: "mail_trash", title: "Move 14 newsletters from example.com to Trash?", detail: "They are all unread and older than a week.", scope_label: "mail from example.com", status: "pending", result: null, decided_by: null, note: null, created_at: ago(8), expires_at: ahead(60 * 24), decided_at: null, ...over });
const APPROVALS = [approval(5)];

const change = (id, over = {}) => ({ id: String(id), title: "Match mail rules on subject words", what: "Let a mail rule match words in the subject line, not only the sender.\nShow the matched words in the rule label.", why: "Newsletters from many senders share subject words, so one rule could replace five.", kind: "feature", revises: null, status: "proposed", approval_id: String(40 + id), feedback: null, stage: "", issue_url: null, pr_url: null, result: null, attempts: 1, previous_result: null, previous_pr_url: null, created_at: ago(300), updated_at: ago(20), finished_at: null, ...over });
const CHANGES = [
  change(1),
  change(2, { title: "Read the weather before suggesting a walk", kind: "improvement", status: "working", stage: "PR open, in review", issue_url: "https://github.com/example/hekate/issues/12", pr_url: "https://github.com/example/hekate/pull/13" }),
  change(7, { title: "Remind me to stretch after long calls", status: "queued" }),
  change(3, { title: "Snooze reminders by voice", status: "merged", result: 'Merged and deployed. Say "snooze that for an hour" to try it.', finished_at: ago(90), pr_url: "https://github.com/example/hekate/pull/9" }),
  change(4, { title: "Rewrite the whole scheduler", kind: "improvement", status: "denied", feedback: "Too big for one change. Try the retry logic alone.", finished_at: ago(600) }),
  change(5, { title: "Fix duplicate calendar events", kind: "fix", status: "failed", stage: "Building", result: "The build failed: tests for recurring events broke.", finished_at: ago(2000), attempts: 2, previous_result: "Main moved before it could merge." }),
  change(6, { title: "Add a haiku mode", status: "expired" }),
];
const IDEAS = [
  { id: "2", text: "A tool to read parcel tracking pages, so I can say when a delivery arrives.", kind: "tool", status: "open", created_at: ago(200), updated_at: ago(200) },
  { id: "1", text: "Weather lookups time out now and then in the morning; retry once before giving up.", kind: "fix", status: "open", created_at: ago(3000), updated_at: ago(3000) },
  { id: "3", text: "Offer to pick a playlist when the evening is free.", kind: "improvement", status: "dismissed", created_at: ago(9000), updated_at: ago(8000) },
];

const weather = {
  place: "Home",
  unit: "F",
  now: { temp: 48, feels_like: 44, condition: "Light rain", code: 61 },
  today: { high: 55, low: 41, precip_chance: 80, sunrise: atDay(0, 7, 20), sunset: atDay(0, 18, 10) },
  hours: Array.from({ length: 8 }, (_, i) => ({ time: iso(NOW + i * 3 * 60 * MIN), temp: 48 + (i < 4 ? i * 2 : 8 - i * 2), precip_chance: [80, 70, 40, 20, 10, 10, 0, 0][i], code: i < 3 ? 61 : i < 5 ? 3 : 1 })),
  days: Array.from({ length: 6 }, (_, i) => ({ date: dateKey(i + 1), high: 55 + i, low: 41 - (i % 3), precip_chance: [30, 10, 0, 60, 90, 20][i], code: [3, 1, 0, 61, 63, 2][i] })),
};

const HISTORY = [
  ["user", "Good morning!", 600],
  ["assistant", "Morning, Rika. Rain until about two, so take the umbrella to the dentist at three. Rent is overdue, and the landlord wrote about the lease.", 599],
  ["user", "Can you move my stand-up reminder to 9:15?", 560],
  ["assistant", "Done. I'll nudge you at 9:15 every weekday from tomorrow.", 559],
  ["user", "what's on the grocery list", 420],
  ["assistant", "Oat milk, eggs and a sourdough starter. Want me to add anything else?", 419],
  ["user", "add bananas", 418],
  ["assistant", "Added bananas to Groceries.", 417],
  ["user", "Is Mina free Saturday? She said something about the lake.", 200],
  ["assistant", "She suggested the lake on Saturday in her last email. Your calendar is clear, so I can pencil in 10 to 4 if you like.", 199],
  ["user", "yes please", 190],
  ["assistant", "Lake day with Mina is on your calendar for Saturday, 10 am to 4 pm.", 189],
].map(([role, text, min], i) => ({ id: String(100 + i), role, text, turn_id: `t${i >> 1}`, created_at: ago(min) }));

// ---- threads --------------------------------------------------------------------------------

const thread = (id, title, over = {}) => ({ id, title, model: "deep", model_name: "glm-5.3", origin: "owner", running: false, archived: false, preview: "", created_at: ago(900), updated_at: ago(30), ...over });
const THREADS = [
  thread("th_aa11", "Compare two ways to back up the lab", { running: true, updated_at: ago(1), preview: "I'm reading how restic and borg handle deduplication before I write anything." }),
  thread("th_bb22", "Set up qBittorrent", { preview: "qBittorrent runs at http://lab.example.com:8080. The login is in the lab inventory.", updated_at: ago(42) }),
  thread("th_cc33", "Which router should I buy?", { model: "chat", origin: "renamon", preview: "Short version: the middle one. It has the ports you listed and no cloud account.", updated_at: ago(60 * 26) }),
];
const THREAD_TURN = "tt_1";
const THREAD_MESSAGES = [
  { id: "m1", role: "user", text: "Set up qBittorrent in the lab and tell me where to find it.", turn_id: THREAD_TURN, created_at: ago(50) },
  {
    id: "m2",
    role: "assistant",
    turn_id: THREAD_TURN,
    created_at: ago(42),
    text: [
      "## qBittorrent is running",
      "",
      "I installed it with Docker Compose in `/opt/stacks/qbittorrent`. Here is what I did:",
      "",
      "- pulled the image and checked its digest",
      "- put the web UI on port **8080**, bound to the lab network only",
      "- saved the service to the inventory so I find it next time",
      "",
      "```yaml",
      "services:",
      "  qbittorrent:",
      "    image: lscr.io/linuxserver/qbittorrent:latest",
      "    ports: ['8080:8080']   # web UI, lab network only, never forwarded to the internet",
      "    volumes: ['/opt/stacks/qbittorrent/config:/config', '/srv/downloads:/downloads']",
      "```",
      "",
      "Open it at [lab.example.com:8080](http://lab.example.com:8080). The first login uses a temporary password from the container log; I changed it and wrote where it lives in the inventory.",
    ].join("\n"),
  },
];
const THREAD_STEPS = [
  ["lab_shell", "Running a command in the lab", "ok", "mkdir -p /opt/stacks/qbittorrent && cd /opt/stacks/qbittorrent"],
  ["lab_write_file", "Writing compose.yaml", "ok", "wrote 412 bytes to /opt/stacks/qbittorrent/compose.yaml"],
  ["lab_shell", "Running a command in the lab", "ok", "Container qbittorrent Started"],
  ["lab_shell", "Running a command in the lab", "error", "curl: (7) Failed to connect to localhost port 8080: Connection refused"],
  ["lab_shell", "Running a command in the lab", "ok", "HTTP/1.1 200 OK"],
  ["web_search", "Searching the web", "ok", "qBittorrent docker temporary password first login"],
  ["lab_write_file", "Updating the inventory", "ok", "wrote 1.2 KB to /opt/stacks/INVENTORY.md"],
].map(([tool, label, status, summary], i) => ({ turn_id: THREAD_TURN, tool, label, status, summary, created_at: ago(49 - i) }));

// ---- page wiring ----------------------------------------------------------------------------

// mode: "full" (fixtures), "empty" (tasks, reminders and mail with nothing), "nomail" (mail not
// configured), "down" (service unreachable)
const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const READY = { type: "ready", client_id: "c1", conversation_id: "k1", capabilities: { stt: true, tts: false }, gestures: ["waving", "nodding"], timezone: Intl.DateTimeFormat().resolvedOptions().timeZone };
const REPLY = "Rain until about two, so take the umbrella to the dentist at three. Rent is overdue, and Dr. Okafor's office wrote to ask about moving Tuesday.";

async function open(browser, viewport, query, { mode = "full", onSay } = {}) {
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  const sockets = [];
  page.on("pageerror", (e) => console.log("pageerror:", e.message));
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    if (mode === "down") return void ws.close();
    sockets.push(ws);
    ws.send(JSON.stringify(READY));
    ws.onMessage((raw) => {
      if (typeof raw !== "string") return;
      const m = JSON.parse(raw);
      if (m.type === "say") onSay?.(ws, m);
    });
  });
  const empty = mode === "empty";
  await page.route("**/healthz", (route) => (mode === "down" ? route.fulfill({ status: 502, body: "bad gateway" }) : json(route, { ok: true, version: "dev" })));
  await page.route("**/api/**", (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;
    const q = url.searchParams;
    if (p === "/api/ws") return route.fallback();
    // As the proxy answers with nothing behind it: a plain-text 502, not the service's JSON.
    if (mode === "down") return route.fulfill({ status: 502, contentType: "text/plain", body: "bad gateway" });
    if (p === "/api/tasks") {
      const status = q.get("status") ?? "open";
      let list = empty ? [] : TASKS.filter((t) => (q.get("list") ? t.list === q.get("list") : true));
      list = list.filter((t) => (status === "all" ? true : status === "done" ? t.completed_at : !t.completed_at));
      return json(route, { tasks: list });
    }
    if (p === "/api/lists") return json(route, { lists: empty ? [] : LISTS });
    if (p === "/api/reminders") {
      const status = q.get("status") ?? "upcoming";
      return json(route, { reminders: empty ? [] : REMINDERS.filter((r) => (status === "all" ? true : status === "past" ? r.status !== "scheduled" : r.status === "scheduled")) });
    }
    if (p === "/api/calendar/status") return json(route, { configured: true, calendar_name: "Personal" });
    if (p === "/api/calendar/events") {
      const from = Date.parse(q.get("from") ?? "") || 0;
      const to = Date.parse(q.get("to") ?? "") || Infinity;
      return json(route, { events: EVENTS.filter((e) => Date.parse(e.start) < to && Date.parse(e.end) > from) });
    }
    if (p === "/api/weather") return json(route, weather);
    if (p === "/api/memory") return json(route, { memories: MEMORIES });
    if (p === "/api/memory/core") return json(route, { entries: CORE });
    if (p === "/api/journal") return json(route, { days: JOURNAL });
    if (p === "/api/changes") return json(route, { changes: CHANGES });
    if (p === "/api/ideas") return json(route, { ideas: IDEAS });
    if (p === "/api/routines") return json(route, { heartbeat: true, waking_start: "07:30", waking_end: "22:30", checkin: true, checkin_at: "00:00" });
    if (p === "/api/approvals") return json(route, { approvals: q.get("status") === "recent" ? [approval(3, { status: "done", result: "Moved 3 messages to Trash", decided_by: "owner", decided_at: ago(40) })] : APPROVALS });
    if (p === "/api/approvals/rules") return json(route, { rules: [{ id: "2", kind: "mail_trash", scope_label: "mail from example.com", uses: 3, created_at: ago(5000) }, { id: "3", kind: "mail_unsubscribe", scope_label: "Trailhead Weekly", uses: 0, created_at: ago(900) }] });
    if (p === "/api/mail/status") return json(route, mode === "nomail" ? { configured: false, accounts: [] } : { configured: true, accounts: [{ name: "personal", address: "rika@example.com", ok: true, error: "", last_sweep_at: ago(4), inbox_count: 212 }, { name: "work", address: "rika@example.org", ok: true, error: "", last_sweep_at: ago(4), inbox_count: 31 }] });
    if (p === "/api/mail/summary") return json(route, empty ? { unread: {}, attention: [] } : { unread: { needs_reply: 1, personal: 1, newsletter: 12, promo: 4 }, attention: [MAIL[0], MAIL[1]] });
    if (p === "/api/mail/messages") {
      const cat = q.get("category");
      return json(route, { messages: empty ? [] : MAIL.filter((m) => !cat || m.category === cat) });
    }
    const one = p.match(/^\/api\/mail\/messages\/(\d+)$/);
    if (one) return json(route, { message: MAIL.find((m) => m.id === one[1]) ?? MAIL[0], body: { text: MAIL_BODY, truncated: false } });
    if (p === "/api/mail/rules") return json(route, { rules: MAIL_RULES });
    if (p === "/api/threads") return json(route, { threads: empty ? [] : q.get("archived") === "true" ? [] : THREADS });
    if (/^\/api\/threads\/[^/]+$/.test(p)) return json(route, THREADS.find((t) => p.endsWith(t.id)) ?? THREADS[1]);
    if (/^\/api\/threads\/[^/]+\/messages$/.test(p)) {
      const bb = p.includes("th_bb22");
      return json(route, { messages: bb ? THREAD_MESSAGES : [{ ...THREAD_MESSAGES[0], id: "m9", turn_id: "tt_live", text: "Compare restic and borg for the lab's nightly backups." }], steps: bb ? THREAD_STEPS : [] });
    }
    if (p === "/api/conversation/messages") return json(route, { messages: HISTORY });
    if (p === "/api/push/key") return json(route, { public_key: "BExampleKeyExampleKeyExampleKeyExampleKeyExampleKeyExampleKeyExampleKeyExampleKeyExampleKeyExample" });
    return route.fulfill({ status: 204 });
  });
  await page.goto(BASE + (query ? `?${query}` : ""));
  await page.waitForSelector("[data-stage=ready]", { timeout: 60_000 }).catch(() => console.log("stage never became ready:", query));
  await page.waitForTimeout(1500);
  return { page, sockets };
}

let vname = "";
async function shot(page, name, wait = 600) {
  await page.waitForTimeout(wait);
  const file = path.join(out, `tour-${vname}-${name}.png`);
  await page.screenshot({ path: file });
  console.log("wrote", file);
}

// Captures a scrolling menu panel top to bottom, one viewport-height shot per step.
async function shotScrolled(page, name) {
  const handle = await page.evaluateHandle(() => {
    return document.querySelector("[role=dialog] [class*=content]");
  });
  const el = handle.asElement();
  if (!el) return shot(page, name);
  const { sh, ch } = await el.evaluate((n) => ({ sh: n.scrollHeight, ch: n.clientHeight }));
  const steps = Math.ceil(sh / (ch * 0.9));
  for (let i = 0; i < steps; i++) {
    await el.evaluate((n, top) => n.scrollTo(0, top), i * ch * 0.9);
    await shot(page, `${name}-${i + 1}`, 350);
  }
}

// ---- the tour -------------------------------------------------------------------------------

const browser = await launch();
for (const [name, viewport] of [
  ["phone", { width: 390, height: 844 }],
  ["desktop", { width: 1280, height: 800 }],
]) {
  vname = name;
  let page;
  let sockets;
  const send = (m) => sockets.forEach((ws) => ws.send(JSON.stringify(m)));

  // 1. The stage at rest, then the dock in type mode.
  ({ page } = await open(browser, viewport, ""));
  await shot(page, "stage-talk");
  await page.getByRole("button", { name: "Type instead" }).click();
  await shot(page, "stage-type");
  await page.close();

  // 2. A conversation turn: caption, thinking, then the reply with cards beside her.
  ({ page, sockets } = await open(browser, viewport, "", {
    onSay: (ws, m) => {
      ws.send(JSON.stringify({ type: "turn_start", turn_id: "t1", input_id: m.id, origin: "c1", user_text: m.text }));
      ws.send(JSON.stringify({ type: "activity", turn_id: "t1", tool: "calendar", label: "Checking your calendar", state: "start" }));
    },
  }));
  await page.getByRole("button", { name: "Type instead" }).click();
  await page.getByRole("textbox", { name: "Message" }).fill("What does my day look like?");
  await page.getByRole("button", { name: "Send" }).click();
  await shot(page, "turn-thinking", 900);
  send({ type: "activity", turn_id: "t1", tool: "calendar", label: "Checking your calendar", state: "done" });
  const cards = [
    { kind: "weather", id: "weather", title: "Weather: Home", data: weather },
    { kind: "events", id: "events:today", title: "Today", data: { from: atDay(0, 0), to: atDay(1, 0), items: EVENTS.slice(0, 3), highlight: [] } },
    { kind: "tasks", id: "tasks:due", title: "Due today", data: { items: TASKS.slice(0, 5), highlight: ["1"] } },
  ];
  for (const card of cards) send({ type: "card", turn_id: "t1", card });
  send({ type: "segment", turn_id: "t1", seq: 0, text: REPLY, duration: 14, audio_b64: null, audio_format: null, words: null, gestures: [{ name: "nodding", at: 0 }], expressions: [], visemes: null });
  send({ type: "turn_end", turn_id: "t1", status: "done", text: REPLY, model: "m", usage: { input_tokens: 0, output_tokens: 0, cost_usd: null } });
  await shot(page, "turn-reply", 5500);
  await page.close();

  // 3. An approval arriving, and a reminder.
  ({ page, sockets } = await open(browser, viewport, ""));
  const ap = approval(5);
  send({ type: "notify", id: "n-approval", kind: "approval", title: "Needs your OK", body: ap.title, card: { kind: "approval", id: "approval-5", title: ap.title, data: ap } });
  await page.getByRole("article").first().waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "notify-approval", 900);
  await page.close();
  ({ page, sockets } = await open(browser, viewport, ""));
  send({ type: "notify", id: "n-reminder", kind: "reminder", title: "Reminder", body: "Leave for the dentist", card: { kind: "reminders", id: "reminder-1", title: "Reminder", data: { items: [REMINDERS[0]], highlight: ["1"] } } });
  await page.getByRole("article").first().waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "notify-reminder", 900);
  await page.close();

  // 4. The conversation log.
  ({ page } = await open(browser, viewport, ""));
  await page.getByRole("button", { name: "Open conversation log" }).first().click();
  await shot(page, "log", 1000);
  await page.close();

  // 5. Menu sections.
  for (const section of ["today", "tasks", "reminders", "calendar"]) {
    ({ page } = await open(browser, viewport, `menu=${section}`));
    await shot(page, section, 1200);
    if (section === "today") await shotScrolled(page, "today-scroll");
    await page.close();
  }

  ({ page } = await open(browser, viewport, "menu=tasks"));
  await page.getByText(LONG_TITLE.slice(0, 30)).first().waitFor({ timeout: 10_000 }).catch(() => {});
  await page.getByRole("button", { name: /Groceries/ }).first().click().catch(() => {});
  await shot(page, "tasks-list-groceries", 800);
  await page.close();

  ({ page } = await open(browser, viewport, "menu=mail"));
  await page.getByText("Needs your attention").waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "mail", 800);
  await page.getByRole("button", { name: /Can we move your appointment/ }).first().click().catch(() => {});
  await page.getByText("Okafor Family Clinic").waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "mail-message", 800);
  await page.close();

  ({ page } = await open(browser, viewport, "menu=changes"));
  await page.getByText("Snooze reminders by voice").waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "changes", 800);
  await shotScrolled(page, "changes-scroll");
  await page.close();

  ({ page } = await open(browser, viewport, "menu=memory"));
  await shot(page, "memory", 1000);
  await shotScrolled(page, "memory-scroll");
  await page.getByRole("button", { name: "Diary" }).click().catch(() => console.log("no Diary button"));
  await page.getByText("Quiet Sunday").waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "memory-diary", 800);
  await page.close();

  ({ page } = await open(browser, viewport, "menu=settings"));
  await shot(page, "settings", 1200);
  await shotScrolled(page, "settings-scroll");
  await page.close();

  // 5b. Threads: the list, a finished thread (markdown, folded steps, expanded), and one working.
  ({ page } = await open(browser, viewport, "menu=threads"));
  await page.getByText("Set up qBittorrent").first().waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "threads", 800);
  await page.close();

  ({ page } = await open(browser, viewport, "thread=th_bb22"));
  await page.getByText("qBittorrent is running").waitFor({ timeout: 10_000 }).catch(() => {});
  await shot(page, "thread-view", 1000);
  await page.getByText("7 steps").click().catch(() => console.log("no steps toggle"));
  await page.evaluate(() => document.querySelector("[class*=transcript]")?.scrollTo(0, 1e6));
  await shot(page, "thread-steps", 600);
  await page.close();

  ({ page, sockets } = await open(browser, viewport, "thread=th_aa11"));
  await page.getByText("Compare restic and borg").waitFor({ timeout: 10_000 }).catch(() => {});
  const ev = (event) => send({ type: "thread_event", thread: "th_aa11", event });
  ev({ type: "turn_start", turn_id: "tt_live", input_id: "x", origin: "c1", user_text: "Compare restic and borg for the lab's nightly backups." });
  ev({ type: "activity", turn_id: "tt_live", tool: "web_search", label: "Searching the web", state: "start" });
  ev({ type: "activity", turn_id: "tt_live", tool: "web_search", label: "Searching the web", state: "done" });
  ev({ type: "segment", turn_id: "tt_live", seq: 0, text: "Both deduplicate well. Restic is one static binary and encrypts by default.", duration: 0, audio_b64: null, audio_format: null, words: null, gestures: [], expressions: [] });
  ev({ type: "activity", turn_id: "tt_live", tool: "web_read", label: "Reading the borg docs", state: "start" });
  await shot(page, "thread-live", 900);
  await page.close();

  // 6. Empty states.
  for (const section of ["tasks", "reminders", "mail", "threads"]) {
    ({ page } = await open(browser, viewport, `menu=${section}`, { mode: "empty" }));
    await shot(page, `empty-${section}`, 1500);
    await page.close();
  }

  ({ page } = await open(browser, viewport, "menu=mail", { mode: "nomail" }));
  await shot(page, "empty-mail-not-set-up", 1500);
  await page.close();

  // 7. The service unreachable.
  ({ page } = await open(browser, viewport, "menu=today", { mode: "down" }));
  await shot(page, "error-today", 2500);
  await page.close();
  ({ page } = await open(browser, viewport, "", { mode: "down" }));
  await shot(page, "error-stage", 2500);
  await page.close();
}
await browser.close();
console.log(`wrote ${out}/`);
