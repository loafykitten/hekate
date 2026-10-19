// Shared setup for the verification scripts: headless system Chrome (software WebGL) against the
// running dev server. The page exposes the Character as `window.hekate` in dev builds; these
// scripts read its private fields (blend, gesture, loopAction, headLayer) on purpose.
import { chromium } from "playwright-core";

export const BASE = process.env.HEKATE_URL ?? "http://localhost:5317/";

export async function launch() {
  return chromium.launch({
    channel: "chrome",
    headless: true,
    args: ["--use-angle=swiftshader", "--enable-unsafe-swiftshader", "--autoplay-policy=no-user-gesture-required"],
  });
}

// Opens the app with query params and waits until the character has loaded.
export async function openCharacter(browser, query = "", viewport = { width: 1000, height: 800 }) {
  const page = await browser.newPage({ viewport });
  page.on("pageerror", (e) => console.log("pageerror:", e.message));
  await page.goto(BASE + (query ? `?${query}` : ""));
  await page.waitForFunction(() => window.hekate, null, { timeout: 30_000 });
  return page;
}
