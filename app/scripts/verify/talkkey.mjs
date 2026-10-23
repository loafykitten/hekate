// Hold P to talk, in a real browser with a fake microphone: on the stage, P held sends push-to-talk
// on release and a quick tap sends nothing; typing a "p" in a field never talks; with a thread
// open, P talks to that thread. The socket and REST are answered here with fixtures.
// Usage: node scripts/verify/talkkey.mjs
import { BASE } from "./common.mjs";
import { chromium } from "playwright-core";

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const READY = { type: "ready", client_id: "c1", conversation_id: "default", capabilities: { stt: true, tts: false }, gestures: [], timezone: "UTC" };
const now = new Date().toISOString();
const THREAD = {
  id: "th_1", title: "Arch mirrors", model: "deep", model_name: "glm-5.3", origin: "owner", running: false,
  archived: false, preview: "", created_at: now, updated_at: now,
};

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await chromium.launch({
  channel: "chrome",
  headless: true,
  args: ["--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--use-fake-ui-for-media-stream", "--use-fake-device-for-media-stream"],
});
const context = await browser.newContext({ viewport: { width: 1280, height: 800 } });
await context.grantPermissions(["microphone"]);
const page = await context.newPage();
page.on("pageerror", (e) => console.log("pageerror:", e.message));

const sent = [];
let audioFrames = 0;
await page.route("**/api/**", async (route) => {
  const p = new URL(route.request().url()).pathname;
  if (p === "/api/ws") return route.fallback();
  if (p === "/api/threads") return json(route, { threads: [THREAD] });
  if (p === "/api/threads/th_1") return json(route, THREAD);
  if (p === "/api/threads/th_1/messages") return json(route, { messages: [], steps: [] });
  if (p === "/api/conversation/messages") return json(route, { messages: [] });
  if (p === "/api/approvals") return json(route, { approvals: [] });
  if (p === "/api/approvals/rules") return json(route, { rules: [] });
  return route.fulfill({ status: 204 });
});
await page.routeWebSocket(/\/api\/ws$/, (ws) => {
  ws.send(JSON.stringify(READY));
  ws.onMessage((raw) => {
    if (typeof raw !== "string") {
      audioFrames++;
      return;
    }
    const m = JSON.parse(raw);
    sent.push(m);
    // Nothing heard: the stage takes new speech again.
    if (m.type === "ptt_end") ws.send(JSON.stringify({ type: "transcript", id: m.id, text: "" }));
  });
});

const of = (type) => sent.filter((m) => m.type === type);
async function hold(ms) {
  await page.keyboard.down("p");
  await page.waitForTimeout(ms);
  await page.keyboard.up("p");
  await page.waitForTimeout(600);
}

// The stage.
await page.goto(BASE);
await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
await page.waitForTimeout(800);
await hold(1200);
check(of("ptt_start").length === 1 && !of("ptt_start")[0].thread, "P held on the stage starts push-to-talk for the stage");
check(of("ptt_end").length === 1 && of("ptt_end")[0].id === of("ptt_start")[0].id, "releasing P sends it");
check(audioFrames > 0, `audio went out while held (${audioFrames} frames)`);

await hold(100);
check(of("ptt_start").length === 2 && of("ptt_end").length === 1 && of("ptt_cancel").length === 1, "a quick tap of P is cancelled, not sent");

// A thread, opened the way a notification does.
await page.goto(BASE + "?thread=th_1");
await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
await page.waitForSelector("textarea[aria-label=Message]", { timeout: 10_000 });
await page.waitForTimeout(600);
sent.length = 0;
await page.click("textarea[aria-label=Message]");
await hold(800);
const typed = await page.inputValue("textarea[aria-label=Message]");
check(of("ptt_start").length === 0 && typed.includes("p"), `P typed into the message field doesn't talk (field: ${JSON.stringify(typed)})`);

await page.locator("textarea[aria-label=Message]").blur();
await hold(1200);
check(of("ptt_start").length === 1 && of("ptt_start")[0].thread === "th_1", "with the thread open, P talks to the thread");
check(of("ptt_end").length === 1 && of("ptt_end")[0].thread === "th_1", "and releasing sends it there");
check(of("interrupt").length === 0, "the stage was not interrupted");

await browser.close();
