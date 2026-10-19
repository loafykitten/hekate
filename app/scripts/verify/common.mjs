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
  // Dev builds expose window.hekate; production builds only mark the stage as ready.
  await page.waitForSelector("[data-stage=ready]", { timeout: 30_000 });
  return page;
}

// One-line description of what loaded (the app no longer prints a status line).
export function describeCharacter(page) {
  return page.evaluate(() => {
    const c = window.hekate;
    if (!c) return "production build (character details not exposed)";
    return [
      `mouth: ${c.rig.mouth.map((w) => w.name).join("+")}`,
      c.idleClip ? `idle: ${c.idleClip}` : "procedural idle",
      `${c.gestures.length} gestures`,
    ].join(" · ");
  });
}

// Waits for the connection pill to leave "Connecting" and returns its state and text.
export async function connectionPill(page) {
  await page.waitForSelector('[role=status][data-state]:not([data-state=checking])', { timeout: 15_000 });
  return page.evaluate(() => {
    const el = document.querySelector("[role=status][data-state]");
    return `${el.dataset.state} (${el.textContent})`;
  });
}
