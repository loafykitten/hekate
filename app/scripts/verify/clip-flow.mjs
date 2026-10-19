// Idle -> gesture -> idle -> talk -> idle, reporting which clips are blended in at each stage and
// any animation tracks that failed to bind.
// Usage: node scripts/verify/clip-flow.mjs
import { launch, openCharacter } from "./common.mjs";

const browser = await launch();
const page = await openCharacter(browser, "variant=desktop");
const warnings = [];
page.on("console", (m) => /No target node|THREE/.test(m.text()) && warnings.push(m.text()));
await page.waitForTimeout(1000);

const state = () =>
  page.evaluate(() => {
    const h = window.hekate;
    const active = [...h.blend].filter(([, e]) => e.weight > 0.01).map(([a, e]) => `${a.getClip().name}:${e.weight.toFixed(2)}`);
    return JSON.stringify({ active, gesture: h.gesture?.action.getClip().name ?? null, headLayer: +h.headLayer.toFixed(2) });
  });

console.log("status:", await page.textContent("#status"));
console.log("idle:          ", await state());
await page.selectOption("#gesture", "waving");
await page.waitForTimeout(1500);
console.log("waving +1.5s:  ", await state());
await page.waitForTimeout(4200);
console.log("waving done:   ", await state());
await page.fill("#say-text", "Sure, I can help with that. Let me take a look at your schedule for today.");
await page.click("button[type=submit]");
await page.waitForSelector("#bubble:not([hidden])", { timeout: 20_000 });
await page.waitForTimeout(1200);
console.log("speaking:      ", await state());
await page.waitForFunction(() => document.querySelector("#bubble").hidden, null, { timeout: 30_000 });
await page.waitForTimeout(800);
console.log("after speech:  ", await state());
console.log("binding warnings:", warnings.length ? warnings : "none");
await browser.close();
