// Viseme lip sync on the real character: a spoken line arrives with timed mouth shapes, and the
// script samples the face morphs while it plays (dev builds expose the Character as
// window.hekate): shapes come in order, each held vowel shows as its own morph, and the
// loudness-only jaw stays shut.
// Face close-ups go to outDir. The audio is a generated tone shaped like two words.
// Usage: node scripts/verify/lipsync.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { BASE, launch } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });

const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const READY = { type: "ready", client_id: "c1", conversation_id: "k1", capabilities: { stt: true, tts: true }, gestures: ["waving"], timezone: "UTC" };
const TEXT = "Hello, Rika.";
// Two "words": loud from 0.1 to 0.6 s and from 0.75 to 1.35 s, silent around them.
const DURATION = 1.6;
const VISEMES = [
  { v: "E", at: 0.1 }, { v: "nn", at: 0.25 }, { v: "O", at: 0.35 }, { v: "sil", at: 0.6 },
  { v: "RR", at: 0.75 }, { v: "I", at: 0.85 }, { v: "kk", at: 1.0 }, { v: "aa", at: 1.1 }, { v: "sil", at: 1.35 },
];

// 16-bit mono WAV of a 220 Hz tone, on during the words.
function wav() {
  const rate = 16000;
  const n = Math.round(DURATION * rate);
  const buf = Buffer.alloc(44 + n * 2);
  buf.write("RIFF", 0); buf.writeUInt32LE(36 + n * 2, 4); buf.write("WAVE", 8); buf.write("fmt ", 12);
  buf.writeUInt32LE(16, 16); buf.writeUInt16LE(1, 20); buf.writeUInt16LE(1, 22); buf.writeUInt32LE(rate, 24);
  buf.writeUInt32LE(rate * 2, 28); buf.writeUInt16LE(2, 32); buf.writeUInt16LE(16, 34); buf.write("data", 36); buf.writeUInt32LE(n * 2, 40);
  for (let i = 0; i < n; i++) {
    const t = i / rate;
    const on = (t >= 0.1 && t < 0.6) || (t >= 0.75 && t < 1.35);
    buf.writeInt16LE(on ? Math.round(Math.sin(2 * Math.PI * 220 * t) * 20000) : 0, 44 + i * 2);
  }
  return buf.toString("base64");
}

function check(ok, what) {
  console.log(`${ok ? "ok  " : "FAIL"} ${what}`);
  if (!ok) process.exitCode = 1;
}

const browser = await launch();
// Small, so software WebGL keeps a usable frame rate for sampling.
const page = await browser.newPage({ viewport: { width: 420, height: 420 } });
page.on("pageerror", (e) => console.log("pageerror:", e.message));
await page.route("**/api/**", (route) => {
  const p = new URL(route.request().url()).pathname;
  if (p === "/api/ws") return route.fallback();
  if (p === "/api/approvals/rules") return json(route, { rules: [] });
  if (p === "/api/approvals") return json(route, { approvals: [] });
  if (p === "/api/conversation/messages") return json(route, { messages: [] });
  return route.fulfill({ status: 204 });
});
let turns = 0;
await page.routeWebSocket(/\/api\/ws$/, (ws) => {
  ws.send(JSON.stringify(READY));
  ws.onMessage((raw) => {
    if (typeof raw !== "string") return;
    const m = JSON.parse(raw);
    if (m.type !== "say") return;
    const turn = `t${++turns}`;
    ws.send(JSON.stringify({ type: "turn_start", turn_id: turn, input_id: m.id, origin: "c1", user_text: m.text }));
    ws.send(JSON.stringify({
      type: "segment", turn_id: turn, seq: 0, text: TEXT, duration: DURATION, audio_b64: wav(), audio_format: "wav",
      words: [{ text: "Hello,", start: 0.1, end: 0.6 }, { text: "Rika.", start: 0.75, end: 1.35 }],
      gestures: [], expressions: [], visemes: VISEMES,
    }));
    ws.send(JSON.stringify({ type: "turn_end", turn_id: turn, status: "done", text: TEXT, model: "m", usage: { input_tokens: 1, output_tokens: 1, cost_usd: null } }));
  });
});

await page.goto(BASE + "?view=face&blink=0");
await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
await page.getByRole("button", { name: "Type instead" }).click();
await page.getByRole("textbox", { name: "Message" }).fill("Say hello");
await page.getByRole("button", { name: "Send" }).click();

// Sample every frame while she speaks: the shapes she was given (from the lip track, on the speech
// clock) and the morphs that came out. Software WebGL is slow and uneven, so the checks compare
// each frame's morphs with that frame's shapes rather than with wall-clock windows.
const samples = await page.evaluate(async (duration) => {
  const c = window.hekate;
  const names = ["jawOpen", "viseme_aa", "viseme_E", "viseme_I", "viseme_O", "viseme_U", "viseme_PP"];
  const deadline = performance.now() + 10_000;
  // From the moment the line's shapes start (speaking turns true as soon as audio is queued).
  while (c.visemes === undefined) {
    if (performance.now() > deadline) return [];
    await new Promise((r) => requestAnimationFrame(r));
  }
  const t0 = performance.now();
  const out = [];
  while (performance.now() - t0 < (duration + 0.5) * 1000) {
    await new Promise((r) => requestAnimationFrame(r));
    out.push({ t: (performance.now() - t0) / 1000, open: c.mouthOpen, given: { ...(c.visemes ?? {}) }, morphs: Object.fromEntries(names.map((n) => [n, c.morphWeight(n)])) });
  }
  return out;
}, DURATION);

if (process.env.DUMP) for (const s of samples) console.log(s.t.toFixed(2), s.open.toFixed(2), JSON.stringify(s.given), Object.entries(s.morphs).map(([k, v]) => `${k.replace("viseme_", "")}=${v.toFixed(2)}`).join(" "));
check(samples.length > 15, `sampled ${samples.length} frames while she spoke`);
check(samples.every((s) => s.morphs.jawOpen < 0.02), "the loudness-only jaw stays shut while visemes drive the mouth");

const top = (rec) => Object.entries(rec).sort((a, b) => b[1] - a[1])[0];
const order = VISEMES.map((v) => v.v).filter((v) => v !== "sil");
const seen = [];
for (const s of samples) {
  const t = top(s.given);
  if (t && t[1] >= 0.5 && seen.at(-1) !== t[0]) seen.push(t[0]);
}
let k = 0;
for (const v of seen) k = order.indexOf(v, k) >= 0 ? order.indexOf(v, k) + 1 : Infinity;
check(seen.length >= 2 && k !== Infinity, `shapes arrive in the line's order (${seen.join(" ")})`);

// A vowel held for a frame and spoken at full strength shows as its own morph, above the rest.
const VOWEL = { O: "viseme_O", aa: "viseme_aa", I: "viseme_I", E: "viseme_E" };
const held = samples.filter((s, i) => {
  const t = top(s.given);
  const p = i > 0 ? top(samples[i - 1].given) : undefined;
  return t && VOWEL[t[0]] && t[1] > 0.95 && p && p[0] === t[0] && s.open > 0.35;
});
const right = held.filter((s) => {
  const want = VOWEL[top(s.given)[0]];
  const best = top(Object.fromEntries(Object.entries(s.morphs).filter(([n]) => n.startsWith("viseme_"))));
  return best[0] === want && best[1] > 0.5;
});
if (held.length === 0) console.log("skip held vowels: no frame caught one held (slow software rendering); run again");
else check(right.length >= held.length * 0.8, `held vowels show as their morph (${right.length}/${held.length} frames)`);
const rest = samples.filter((s) => s.t >= DURATION + 0.3);
check(rest.length > 0 && rest.every((s) => Object.values(s.morphs).every((v) => v < 0.05)), "the mouth is at rest after the line");

// Close-ups at the O and the aa, in a second run of the same line.
await page.getByRole("textbox", { name: "Message" }).fill("Again");
await page.getByRole("button", { name: "Send" }).click();
await page.waitForFunction(() => window.hekate.speaking, null, { timeout: 10_000 });
const t0 = Date.now();
for (const [name, at] of [["o", 0.5], ["gap", 0.69], ["aa", 1.25]]) {
  await page.waitForTimeout(Math.max(0, at * 1000 - (Date.now() - t0)));
  const file = path.join(out, `lipsync-${name}.png`);
  await page.screenshot({ path: file });
  console.log("wrote", file);
}
await browser.close();
