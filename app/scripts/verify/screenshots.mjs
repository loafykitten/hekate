// Screenshots of the main views: desktop, phone, mid-speech with bubble, face close-up.
// Usage: node scripts/verify/screenshots.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { launch, openCharacter } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });
const browser = await launch();

const shots = [
  ["desktop", "variant=desktop&blink=0", { width: 1280, height: 800 }],
  ["phone", "blink=0", { width: 390, height: 844 }],
  ["face", "variant=desktop&view=face&blink=0&mouth=0.4", { width: 700, height: 700 }],
];
for (const [name, query, viewport] of shots) {
  const page = await openCharacter(browser, query, viewport);
  await page.waitForTimeout(1200);
  console.log(`${name}: ${await page.textContent("#status")}`);
  await page.screenshot({ path: path.join(out, `${name}.png`) });
  await page.close();
}

const page = await openCharacter(browser, "variant=desktop&blink=0", { width: 1280, height: 800 });
await page.fill("#say-text", "Hello! I'm Renamon. This is a test of my speech bubble and voice.");
await page.click("button[type=submit]");
await page.waitForSelector("#bubble:not([hidden])", { timeout: 20_000 });
await page.waitForTimeout(1200);
await page.screenshot({ path: path.join(out, "speaking.png") });
console.log(`speaking: bubble shows "${await page.textContent("#bubble")}"`);

await browser.close();
console.log(`wrote ${out}/`);
