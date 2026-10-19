// Contact sheet of frames at 0.1 s steps through a gesture's start and end, or through a
// talk-start/talk-stop cycle, for eyeballing transition smoothness.
// Usage: node scripts/verify/transition-strip.mjs [outFile.png] [gesture | "talk"]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { launch, openCharacter } from "./common.mjs";

const out = process.argv[2] ?? "verify-out/transition-strip.png";
mkdirSync(path.dirname(out), { recursive: true });
const scenario = process.argv[3] ?? "waving";
const browser = await launch();
const page = await openCharacter(browser, "variant=desktop&blink=0", { width: 360, height: 520 });
await page.waitForTimeout(400);
const frames = await page.evaluate(async (scenario) => {
  const h = window.hekate;
  const canvas = document.querySelector("#stage");
  const shots = [];
  // Stop the app loop from advancing the character; step it deterministically and let it render.
  const realUpdate = h.update.bind(h);
  h.update = () => {};
  const step = (n) => { for (let i = 0; i < n; i++) realUpdate(1 / 60); };
  const grab = async (label) => {
    await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
    shots.push([label, await createImageBitmap(canvas)]);
  };
  step(120);
  if (scenario === "talk") {
    h.speaking = true;
    for (let i = 0; i < 8; i++) { await grab(`talk +${(i * 0.1).toFixed(1)}s`); step(6); }
    step(120);
    h.speaking = false;
    for (let i = 0; i < 8; i++) { await grab(`stop +${(i * 0.1).toFixed(1)}s`); step(6); }
  } else {
    h.playGesture(scenario);
    for (let i = 0; i < 8; i++) { await grab(`in +${(i * 0.1).toFixed(1)}s`); step(6); }
    const duration = h.animations.find((c) => c.name === scenario).duration;
    step(Math.max(0, Math.round((duration - 1.6) * 60)));
    for (let i = 0; i < 16; i++) { await grab(`end ${(-0.8 + i * 0.1).toFixed(1)}s`); step(6); }
  }
  const cols = 8, cellW = 180, cellH = 260;
  const sheet = document.createElement("canvas");
  sheet.width = cols * cellW;
  sheet.height = Math.ceil(shots.length / cols) * cellH;
  const ctx = sheet.getContext("2d");
  ctx.fillStyle = "#1b1726";
  ctx.fillRect(0, 0, sheet.width, sheet.height);
  shots.forEach(([label, bmp], i) => {
    const x = (i % cols) * cellW, y = Math.floor(i / cols) * cellH;
    const sw = bmp.width * 0.5, sh = bmp.height * 0.62; // crop to the character
    ctx.drawImage(bmp, (bmp.width - sw) / 2, bmp.height * 0.18, sw, sh, x, y, cellW, cellH - 18);
    ctx.fillStyle = "#fff";
    ctx.font = "13px sans-serif";
    ctx.fillText(label, x + 6, y + cellH - 4);
  });
  document.body.replaceChildren(sheet);
  Object.assign(sheet.style, { position: "fixed", left: "0", top: "0" });
  return { width: sheet.width, height: sheet.height };
}, scenario);
await page.setViewportSize(frames);
await page.screenshot({ path: out });
console.log(`wrote ${out}`);
await browser.close();
