import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { OutlineEffect } from "three/addons/effects/OutlineEffect.js";
import { Bubble } from "./bubble";
import { Character } from "./character";
import { loadCharacterSource } from "./manifest";
import { Speech } from "./speech";

const params = new URLSearchParams(location.search);
const canvas = document.querySelector<HTMLCanvasElement>("#stage")!;
const status = document.querySelector<HTMLElement>("#status")!;
const form = document.querySelector<HTMLFormElement>("#say")!;
const input = document.querySelector<HTMLInputElement>("#say-text")!;
const button = form.querySelector("button")!;

const renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
renderer.outputColorSpace = THREE.SRGBColorSpace;

const scene = new THREE.Scene();
scene.add(new THREE.HemisphereLight(0xfff4e6, 0x3a3050, 1.6));
const sun = new THREE.DirectionalLight(0xffffff, 2.2);
sun.position.set(1.5, 3, 4);
scene.add(sun);

const camera = new THREE.PerspectiveCamera(30, 1, 0.05, 50);
const controls = new OrbitControls(camera, canvas);
controls.enableDamping = true;
controls.enablePan = false;

const speech = new Speech();
const bubble = new Bubble(document.querySelector<HTMLElement>("#bubble")!);
let character: Character | undefined;
let bodyHeight = 1.7;

function frame() {
  const aspect = window.innerWidth / window.innerHeight;
  camera.aspect = aspect;
  const fov = THREE.MathUtils.degToRad(camera.fov);
  if (params.get("view") === "face") {
    const y = character ? character.head.getWorldPosition(new THREE.Vector3()).y + 0.12 : bodyHeight * 0.85;
    camera.position.set(0.25, y + 0.05, 0.9);
    controls.target.set(0, y, 0);
  } else {
    // Fit the whole body with headroom for the bubble; on narrow (phone) screens fit the width too.
    const halfH = bodyHeight * 0.72;
    const fitH = halfH / Math.tan(fov / 2);
    const fitW = fitH / Math.min(1, aspect * 1.4);
    const centerY = bodyHeight * 0.62;
    camera.position.set(0, centerY + 0.05, Math.max(fitH, fitW));
    controls.target.set(0, centerY, 0);
  }
  camera.updateProjectionMatrix();
  controls.update();
}

function resize() {
  renderer.setSize(window.innerWidth, window.innerHeight, false);
  frame();
}
window.addEventListener("resize", resize);

let utterance = 0;
let hideTimer: ReturnType<typeof setTimeout> | undefined;

// Newest request wins: a slower synthesis that finishes late is dropped, not played.
async function say(text: string) {
  const id = ++utterance;
  const unlocked = speech.unlock();
  button.disabled = true;
  try {
    if (!(await unlocked)) {
      status.textContent = "Audio is blocked until you interact with the page. Press Say to start.";
      return;
    }
    const audio = await speech.synthesize(text);
    if (id !== utterance) return;
    clearTimeout(hideTimer);
    bubble.show(text);
    button.disabled = false;
    await speech.play(audio);
    if (id === utterance) hideTimer = setTimeout(() => bubble.hide(), 2500);
  } catch (err) {
    if (id === utterance) status.textContent = String(err);
  } finally {
    if (id === utterance) button.disabled = false;
  }
}

form.addEventListener("submit", (e) => {
  e.preventDefault();
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
  void say(text);
});

const forcedMouth = params.has("mouth") ? Number(params.get("mouth")) : undefined;
// Debug: ?morph=viseme_O:1&morph=eyeBlink_L:0.5 pins morph weights after the character updates.
const pinnedMorphs = params.getAll("morph").map((p) => {
  const [name, weight] = p.split(":");
  return { name, weight: Number(weight ?? 1) };
});
const timer = new THREE.Timer();
const anchor = new THREE.Vector3();
let outline: OutlineEffect | undefined;

function loop(time: number) {
  timer.update(time);
  const dt = Math.min(timer.getDelta(), 0.1);
  if (character) {
    // Typical syllables open the jaw about halfway; only the loudest reach full open.
    // Quick attack and a nearly as quick release let it close between syllables instead of gaping.
    const level = speech.level();
    const target = forcedMouth ?? Math.min(1, Math.max(0, (level - 0.2) / 0.8) ** 1.3 * 0.65);
    const rate = target > character.mouthOpen ? 35 : 22;
    character.mouthOpen += (target - character.mouthOpen) * (1 - Math.exp(-rate * dt));
    character.speaking = speech.speaking || forcedMouth !== undefined;
    character.update(dt);
    for (const m of pinnedMorphs) character.setMorph(m.name, m.weight);
    bubble.update(speech.speaking ? speech.progress : 1, character.headScreenAnchor(camera, anchor));
  }
  controls.update();
  (outline ?? renderer).render(scene, camera);
  requestAnimationFrame(loop);
}

async function start() {
  resize();
  requestAnimationFrame(loop);
  const source = await loadCharacterSource(params);
  character = await Character.load(source.modelUrl, source.rig);
  scene.add(character.root);
  outline = new OutlineEffect(renderer);
  if (import.meta.env.DEV) Object.assign(window, { hekate: character });
  if (params.get("blink") === "0") character.blinkEnabled = false;
  bodyHeight = character.bounds().getSize(new THREE.Vector3()).y;
  frame();
  status.textContent = describe(character, source.label);
  if (import.meta.env.DEV) setupGesturePicker(character);
  const gesture = params.get("gesture");
  if (gesture) character.playGesture(gesture);
  const demo = params.get("say");
  if (demo) void say(demo);
}

// Dev-only: try each gesture clip from a dropdown next to the input.
function setupGesturePicker(c: Character) {
  const picker = document.querySelector<HTMLSelectElement>("#gesture")!;
  if (!c.gestures.length) return;
  for (const g of c.gestures) picker.add(new Option(g.replaceAll("_", " "), g));
  picker.hidden = false;
  picker.addEventListener("change", () => {
    c.playGesture(picker.value);
    picker.selectedIndex = 0;
  });
}

function describe(c: Character, variant: string) {
  const parts = [variant, `mouth: ${c.rig.mouth.map((w) => w.name).join("+")}`];
  parts.push(c.idleClip ? `idle: ${c.idleClip}` : "procedural idle");
  if (c.gestures.length) parts.push(`${c.gestures.length} gestures`);
  return parts.join(" · ");
}

start().catch((err) => {
  status.textContent = `Failed to load: ${err}`;
  console.error(err);
});
