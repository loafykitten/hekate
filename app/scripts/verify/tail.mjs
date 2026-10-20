// The tail from the front, side and back over a few seconds, plus numbers: how far the tip
// travels, its lowest point, and how close the outer tail comes to the legs, idle and through
// every gesture. Usage: node scripts/verify/tail.mjs [outDir] [--gestures]
import { mkdirSync } from "node:fs";
import { launch, openCharacter } from "./common.mjs";

const out = process.argv[2] && !process.argv[2].startsWith("--") ? process.argv[2] : "verify-out/tail";
const withGestures = process.argv.includes("--gestures");
mkdirSync(out, { recursive: true });

const browser = await launch();
const page = await openCharacter(browser, "", { width: 800, height: 900 });

// Where the tail joints are now, and the closest outer joint (past the base) to a leg bone.
const sample = () =>
  page.evaluate(() => {
    const c = window.hekate;
    const V = c.root.position.constructor;
    const p = new V();
    const q = new V();
    const legs = [...c.bones.keys()].filter((n) => /thigh|leg_stretch|foot/i.test(n));
    // In her own frame, so turning her for the side and back shots doesn't skew the numbers.
    const joints = c.rig.procedural.tail.map((n) => (c.root.worldToLocal(c.bones.get(n).getWorldPosition(p)), p.toArray()));
    let legGap = Infinity;
    joints.forEach((j, i) => {
      if (i < 2) return; // the base sits against the body by design
      p.fromArray(j);
      for (const l of legs) legGap = Math.min(legGap, p.distanceTo(c.root.worldToLocal(c.bones.get(l).getWorldPosition(q))));
    });
    return { tip: joints.at(-1), lowest: Math.min(...joints.map((j) => j[1])), legGap };
  });

const range = (xs) => `${Math.min(...xs).toFixed(2)}..${Math.max(...xs).toFixed(2)}`;
const report = (label, samples) =>
  console.log(
    `${label}: tip x ${range(samples.map((s) => s.tip[0]))} m, tip height ${range(samples.map((s) => s.tip[1]))} m, ` +
      `lowest joint ${Math.min(...samples.map((s) => s.lowest)).toFixed(2)} m, closest to a leg ${Math.min(...samples.map((s) => s.legGap)).toFixed(2)} m`,
  );

await page.waitForTimeout(1000); // let the first frames settle
const idle = [];
for (const [view, angle] of [["front", 0], ["side", Math.PI / 2], ["back", Math.PI * 0.8]]) {
  // Turning the stage kicks the springs once; give them a moment before the shots.
  await page.evaluate((a) => (window.hekate.root.rotation.y = a), angle);
  await page.waitForTimeout(1500);
  for (let k = 0; k < 3; k++) {
    await page.screenshot({ path: `${out}/${view}-${k}.png` });
    for (let i = 0; i < 6; i++) {
      idle.push(await sample());
      await page.waitForTimeout(200);
    }
  }
}
await page.evaluate(() => (window.hekate.root.rotation.y = 0));
report("idle", idle);

if (withGestures) {
  const clips = await page.evaluate(() => [
    ...window.hekate.rig.clips.gestures.map((name) => ({ name, fidget: false })),
    ...window.hekate.rig.clips.fidgets.map((name) => ({ name, fidget: true })),
  ]);
  const all = [];
  for (const { name, fidget } of clips) {
    // playOneShot is private; it's the path both gestures and fidgets take.
    if (!(await page.evaluate(({ name, fidget }) => window.hekate.playOneShot(name, fidget), { name, fidget }))) {
      console.log(`  ${name}: did not start`);
      continue;
    }
    // Sample the whole clip and the return to the loop after it (capped at 12 s).
    const during = [];
    let after = 0;
    for (let k = 0; k < 48 && after < 4; k++) {
      during.push(await sample());
      if (!(await page.evaluate(() => window.hekate.playing))) after++;
      await page.waitForTimeout(250);
    }
    all.push(...during);
    report(`  ${name}${fidget ? " (fidget)" : ""}`, during);
  }
  report(`all clips (${clips.length})`, all);
}

console.log(`screenshots in ${out}`);
await browser.close();
