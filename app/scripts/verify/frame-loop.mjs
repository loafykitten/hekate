// Regression check: exactly one render loop runs after the character loads. React StrictMode
// mounts, disposes and remounts the stage in dev; a disposed stage must not keep a frame loop
// alive. Counts distinct requestAnimationFrame callbacks over two seconds.
import { BASE, launch } from "./common.mjs";

const browser = await launch();
const page = await browser.newPage();
await page.addInitScript(() => {
  const orig = window.requestAnimationFrame.bind(window);
  window.__rafCallbacks = new Set();
  window.requestAnimationFrame = (cb) => {
    window.__rafCallbacks.add(cb);
    return orig(cb);
  };
});
await page.goto(BASE);
await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
await page.waitForTimeout(500);
await page.evaluate(() => window.__rafCallbacks.clear());
await page.waitForTimeout(2000);
const loops = await page.evaluate(() => window.__rafCallbacks.size);
await browser.close();
if (loops !== 1) {
  console.error(`expected 1 render loop, found ${loops}`);
  process.exit(1);
}
console.log("frame loop: ok (1 loop)");
