// Screenshots of the main views: desktop, phone, face close-up, mouth held open.
// Usage: node scripts/verify/screenshots.mjs [outDir]
import { mkdirSync } from "node:fs";
import path from "node:path";
import { connectionPill, describeCharacter, launch, openCharacter } from "./common.mjs";

const out = process.argv[2] ?? "verify-out";
mkdirSync(out, { recursive: true });
const browser = await launch();

const shots = [
  ["desktop", "variant=desktop&blink=0", { width: 1280, height: 800 }],
  ["phone", "blink=0", { width: 390, height: 844 }],
  ["face", "variant=desktop&view=face&blink=0&mouth=0.4", { width: 700, height: 700 }],
  ["mouth", "variant=desktop&blink=0&mouth=0.6", { width: 1280, height: 800 }],
];
for (const [name, query, viewport] of shots) {
  const page = await openCharacter(browser, query, viewport);
  await page.waitForTimeout(1200);
  console.log(`${name}: ${await describeCharacter(page)}; connection ${await connectionPill(page)}`);
  await page.screenshot({ path: path.join(out, `${name}.png`) });
  await page.close();
}

await browser.close();
console.log(`wrote ${out}/`);
