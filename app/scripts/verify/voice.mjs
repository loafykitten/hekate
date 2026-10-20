// Voice off, phone and desktop: the speaker toggle in the dock (talk and type modes), a quiet
// reply in the bubble, the choice surviving a reload, and the Settings switch. The conversation
// socket is answered here with fixtures and records what the app sends.
// Usage: node scripts/verify/voice.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const READY = { type: "ready", client_id: "c1", conversation_id: "k1", capabilities: { stt: true, tts: true }, gestures: ["waving"], timezone: "UTC" };
const REPLY = "Of course. I'll keep it to the bubble while you're out.";

async function wire(page, sent) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/ws") return route.fallback();
    if (p === "/api/approvals/rules") return json(route, { rules: [] });
    if (p === "/api/approvals") return json(route, { approvals: [] });
    if (p === "/api/routines") return json(route, { heartbeat: true, waking_start: "08:00", waking_end: "22:00" });
    if (p === "/api/conversation/messages") return json(route, { messages: [] });
    if (p === "/api/health") return json(route, { status: "ok", version: "dev" });
    return route.fulfill({ status: 204 });
  });
  await page.routeWebSocket(/\/api\/ws$/, (ws) => {
    ws.send(JSON.stringify(READY));
    ws.onMessage((raw) => {
      if (typeof raw !== "string") return;
      const m = JSON.parse(raw);
      sent.push(m);
      if (m.type !== "say") return;
      const turn = `t-${sent.length}`;
      ws.send(JSON.stringify({ type: "turn_start", turn_id: turn, input_id: m.id, origin: "c1", user_text: m.text }));
      ws.send(JSON.stringify({
        type: "segment", turn_id: turn, seq: 0, text: REPLY, duration: 3.2,
        audio_b64: null, audio_format: null, words: null,
        gestures: [{ name: "waving", at: 0.2 }], expressions: [{ name: "laughs", at: 1 }],
      }));
      ws.send(JSON.stringify({ type: "turn_end", turn_id: turn, status: "done", text: REPLY, model: "m", usage: { input_tokens: 1, output_tokens: 1, cost_usd: null } }));
    });
  });
}

async function shot(page, name) {
  await page.waitForTimeout(400);
  const file = path.join(out, `voice-${name}.png`);
  await page.screenshot({ path: file });
  console.log("wrote", file);
}

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await launch();
for (const [vname, viewport] of [["phone", { width: 375, height: 812 }], ["desktop", { width: 1280, height: 800 }]]) {
  const context = await browser.newContext({ viewport, hasTouch: viewport.width < 600 });
  const sent = [];
  let page = await context.newPage();
  page.on("pageerror", (e) => console.log(`${vname} pageerror:`, e.message));
  await wire(page, sent);
  await page.goto(BASE);
  await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
  await page.evaluate(() => localStorage.removeItem("hekate.dockMode"));

  await shot(page, `${vname}-dock-on`);
  await page.getByRole("button", { name: "Turn her voice off" }).click();
  await page.getByText("Voice off.").waitFor();
  await shot(page, `${vname}-dock-off`);

  await page.getByRole("button", { name: "Type instead" }).click();
  await page.getByRole("textbox", { name: "Message" }).fill("I'm on the train, keep it quiet");
  await shot(page, `${vname}-text-off`);
  await page.getByRole("button", { name: "Send" }).click();
  await page.waitForTimeout(1500);
  await shot(page, `${vname}-quiet-reply`);
  const say = sent.find((m) => m.type === "say");
  check(say?.quiet === true, `${vname}: typed input is sent quiet (${JSON.stringify(say)})`);

  // Remembered on this device.
  await page.close();
  page = await context.newPage();
  await wire(page, sent);
  await page.goto(BASE + "?menu=settings");
  await page.getByText("Speak out loud").waitFor();
  const sw = page.getByRole("switch", { name: /Speak out loud/ });
  check(!(await sw.isChecked()), `${vname}: voice is still off after a reload`);
  await page.getByText("Speak out loud").scrollIntoViewIfNeeded();
  await shot(page, `${vname}-settings-off`);
  await page.getByText("Speak out loud").click();
  check(await sw.isChecked(), `${vname}: the settings row turns it back on`);
  await shot(page, `${vname}-settings-on`);
  await page.close();
  await context.close();
}
await browser.close();
