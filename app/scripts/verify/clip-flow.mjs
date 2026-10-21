// Idle -> gesture -> idle -> speaking (still her idle: gestures carry speech) -> idle, reporting
// which clips are blended in at each stage and
// any animation tracks that failed to bind.
// Usage: node scripts/verify/clip-flow.mjs
import { describeCharacter, launch, openCharacter } from "./common.mjs";

const browser = await launch();
const page = await openCharacter(browser, "variant=desktop");
const warnings = [];
page.on("console", (m) => /No target node|THREE/.test(m.text()) && warnings.push(m.text()));
// The app only talks when audio plays, which needs a TTS service; force the talk state instead.
await page.evaluate(() => {
  const h = window.hekate;
  const update = h.update.bind(h);
  h.update = (dt) => {
    if (window.forceTalk !== undefined) h.speaking = window.forceTalk;
    update(dt);
  };
});
await page.waitForTimeout(1000);

const state = () =>
  page.evaluate(() => {
    const h = window.hekate;
    const active = [...h.blend].filter(([, e]) => e.weight > 0.01).map(([a, e]) => `${a.getClip().name}:${e.weight.toFixed(2)}`);
    return JSON.stringify({ active, gesture: h.gesture?.action.getClip().name ?? null, headLayer: +h.headLayer.toFixed(2) });
  });

console.log("character:", await describeCharacter(page));
console.log("idle:          ", await state());
await page.selectOption('select[aria-label="Play a gesture"]', "waving");
await page.waitForTimeout(1500);
console.log("waving +1.5s:  ", await state());
await page.waitForTimeout(4200);
console.log("waving done:   ", await state());
await page.evaluate(() => (window.forceTalk = true));
await page.waitForTimeout(2000);
console.log("speaking:      ", await state());
await page.evaluate(() => (window.forceTalk = false));
await page.waitForTimeout(2000);
console.log("after speech:  ", await state());
console.log("binding warnings:", warnings.length ? warnings : "none");
await browser.close();
