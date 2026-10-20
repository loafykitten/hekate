// Screenshots of her diary in the Memory section, phone and desktop: entries, the changes a
// reflection made, and undoing one. REST and the socket are answered here with fixtures.
// Usage: node scripts/verify/diary.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const mem = (id, kind, content, over = {}) => ({ id: String(id), kind, content, importance: 3, follow_up_at: null, created_at: "", updated_at: "", ...over });
const fresh = () => [
  {
    day: "2026-10-19",
    diary: { day: "2026-10-19", text: "Rika's interview moved to Thursday. They sounded tired this morning but steadier by evening, and laughed about the oolong. Mika visits Saturday; I want to remember to ask how it went.\n\nA good day, mostly. I like being the steady one.", created_at: "", updated_at: "" },
    reflections: [{
      id: "3", summary: "Saved 1, updated 1 and retired 1 memories", created_at: "",
      changes: [
        { id: "11", action: "update", memory_id: "42", before: mem(42, "fact", "Has a job interview on Friday"), after: mem(42, "follow_up", "Has a job interview on Thursday at 2 pm"), reason: "The interview moved", undone_at: null },
        { id: "12", action: "retire", memory_id: "7", before: mem(7, "preference", "Likes oolong"), after: null, reason: "Merged into a fuller memory", undone_at: null },
        { id: "13", action: "save", memory_id: "51", before: null, after: mem(51, "follow_up", "Ask how Mika's visit went"), reason: "Mika visits Saturday", undone_at: null },
      ],
    }],
  },
  { day: "2026-10-18", diary: { day: "2026-10-18", text: "Quiet Sunday. We sorted the groceries and talked about nothing much. Nice.", created_at: "", updated_at: "" }, reflections: [] },
];

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 375, height: 812 }], ["desktop", { width: 1280, height: 800 }]]) {
  const days = fresh();
  const page = await browser.newPage({ viewport, hasTouch: viewport.width < 600 });
  page.on("pageerror", (e) => console.log(`${vname} pageerror:`, e.message));
  await page.routeWebSocket(/\/api\/ws$/, (ws) => ws.send(JSON.stringify({ type: "ready", client_id: "c1", conversation_id: "k", capabilities: { stt: false, tts: false }, gestures: [], timezone: "UTC" })));
  await page.route("**/api/**", (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/journal") return json(route, { days });
    const undo = p.match(/^\/api\/reflections\/changes\/(\d+)\/undo$/);
    if (undo) {
      const c = days[0].reflections[0].changes.find((x) => x.id === undo[1]);
      c.undone_at = new Date().toISOString();
      return json(route, c);
    }
    if (p === "/api/memory") return json(route, { memories: [] });
    if (p === "/api/memory/core") return json(route, { entries: [] });
    return route.fulfill({ status: 204 });
  });
  await page.goto(BASE + "?menu=memory");
  await page.getByRole("button", { name: "Diary" }).click();
  await page.getByText("Quiet Sunday").waitFor();
  await page.waitForTimeout(400);
  await page.screenshot({ path: path.join(out, `diary-${vname}.png`) });
  await page.getByRole("button", { name: /3 changes to her memories/ }).click();
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(out, `diary-${vname}-changes.png`) });
  await page.getByRole("button", { name: /Undo: Let go of “Likes oolong”/ }).click();
  await page.getByText("Undone", { exact: true }).waitFor();
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(out, `diary-${vname}-undone.png`) });
  console.log(`${vname}: ok`);
  await page.close();
}
await browser.close();
