// Drives the character at a fixed 60 fps step through the crossfade edge cases that have caused
// pops before: a gesture interrupting a gesture mid-fade, re-triggering a gesture that is still
// fading out, double interrupts, and speech flickering mid-gesture.
//
// Pass criteria: weights sum to 1 (deviation ~1e-16), no outgoing action ever gains weight, every
// per-frame bone step stays at or below what the clips do on their own (worst is ~12-22 deg for
// fast forearm-twist bones in waving/smh/excited), and it settles back to the idle alone.
// Usage: node scripts/verify/blend-stress.mjs
import { launch, openCharacter } from "./common.mjs";

const browser = await launch();
const page = await openCharacter(browser, "variant=desktop", { width: 640, height: 480 });
const result = await page.evaluate(() => {
  const h = window.hekate;
  const bones = [];
  h.root.traverse((o) => o.isBone && bones.push(o));
  let prev = bones.map((b) => b.quaternion.clone());
  const worst = {};
  let sumDeviation = 0;
  let rebounds = 0;
  let lastWeights = new Map();
  const step = (label, frames = 1) => {
    for (let i = 0; i < frames; i++) {
      h.update(1 / 60);
      bones.forEach((b, j) => {
        const deg = (b.quaternion.angleTo(prev[j]) * 180) / Math.PI;
        worst[label] = Math.max(worst[label] ?? 0, +deg.toFixed(2));
        prev[j] = b.quaternion.clone();
      });
      let sum = 0;
      for (const [a, e] of h.blend) {
        sum += e.weight;
        if (a !== h.blendTarget && lastWeights.has(a) && e.weight > lastWeights.get(a) + 1e-9) rebounds++;
      }
      sumDeviation = Math.max(sumDeviation, Math.abs(sum - 1));
      lastWeights = new Map([...h.blend].map(([a, e]) => [a, e.weight]));
    }
  };
  step("settle", 60);
  h.playGesture("waving"); step("waving", 120);
  h.playGesture("thankful"); step("interrupt", 6);
  h.playGesture("waving"); step("restart while fading", 30);
  h.playGesture("dismissing"); step("A", 5);
  h.playGesture("no"); step("B", 1);
  h.playGesture("excited"); step("C (double interrupt)", 30);
  h.playGesture("waving"); step("w", 3); h.playGesture("no"); step("n", 2);
  h.playGesture("waving"); step("w again", 3); h.playGesture("no"); step("n again", 30);
  for (let k = 0; k < 6; k++) { h.speaking = k % 2 === 0; step("speech flicker", 5); }
  h.speaking = false; step("drain", 600);
  return {
    worstStepDegByPhase: worst,
    weightSumDeviation: sumDeviation.toExponential(2),
    outgoingRebounds: rebounds,
    finalBlend: [...h.blend].map(([a, e]) => `${a.getClip().name}:${e.weight.toFixed(2)}`),
  };
});
console.log(JSON.stringify(result, null, 1));
await browser.close();
