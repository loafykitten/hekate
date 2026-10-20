import * as THREE from "three";
import { OrbitControls } from "three/addons/controls/OrbitControls.js";
import { OutlineEffect } from "three/addons/effects/OutlineEffect.js";
import { Bubble } from "./bubble";
import { Character } from "./character";
import { LipTrack } from "./lips";
import { loadCharacterSource } from "./manifest";
import { Speech } from "./speech";
import { warmCharacterCache } from "./warm";

// The scene around the character: renderer, camera framing, the animation loop, mouth
// smoothing and the debug URL params. Framework-free; the React shell only owns its lifecycle.

export interface PinnedMorph {
  name: string;
  weight: number;
}

export interface StageParams {
  view: "body" | "face";
  forcedMouth?: number;
  pinnedMorphs: PinnedMorph[];
  gesture?: string;
  blink: boolean;
}

// Debug URL params: ?view=face, ?mouth=0..1 (pin the mouth open), ?morph=viseme_O:1&morph=eyeBlink_L:0.5
// (pin morph weights after the character updates), ?gesture=waving, ?blink=0.
// (?variant= is read by loadCharacterSource.)
export function parseStageParams(search: URLSearchParams): StageParams {
  const mouth = search.get("mouth");
  return {
    view: search.get("view") === "face" ? "face" : "body",
    forcedMouth: mouth === null ? undefined : Number(mouth),
    pinnedMorphs: search.getAll("morph").map((p) => {
      const [name, weight] = p.split(":");
      return { name, weight: Number(weight ?? 1) };
    }),
    gesture: search.get("gesture") ?? undefined,
    blink: search.get("blink") !== "0",
  };
}

export interface FramingInput {
  aspect: number;
  fovDegrees: number;
  bodyHeight: number;
  // Face close-up; headY is the head bone's world height once the character is loaded.
  face?: { headY?: number };
}

export interface Framing {
  position: [number, number, number];
  target: [number, number, number];
}

export const DEFAULT_BODY_HEIGHT = 1.7;

// Where to put the camera. Full body: fit the whole body with headroom for the bubble, and on
// narrow (phone) screens fit the width too. Face: a close-up from slightly to the side.
export function computeFraming({ aspect, fovDegrees, bodyHeight, face }: FramingInput): Framing {
  if (face) {
    const y = face.headY !== undefined ? face.headY + 0.12 : bodyHeight * 0.85;
    return { position: [0.25, y + 0.05, 0.9], target: [0, y, 0] };
  }
  const halfH = bodyHeight * 0.72;
  const fitH = halfH / Math.tan(THREE.MathUtils.degToRad(fovDegrees) / 2);
  const fitW = fitH / Math.min(1, aspect * 1.4);
  const centerY = bodyHeight * 0.62;
  return { position: [0, centerY + 0.05, Math.max(fitH, fitW)], target: [0, centerY, 0] };
}

// Typical syllables open the jaw about halfway; only the loudest reach full open.
export function mouthTarget(level: number, forced?: number): number {
  return forced ?? Math.min(1, Math.max(0, (level - 0.2) / 0.8) ** 1.3 * 0.65);
}

// Quick attack and a nearly as quick release let the mouth close between syllables instead of gaping.
export function smoothMouth(current: number, target: number, dt: number): number {
  const rate = target > current ? 35 : 22;
  return current + (target - current) * (1 - Math.exp(-rate * dt));
}

function disposeObject(root: THREE.Object3D) {
  root.traverse((o) => {
    const mesh = o as THREE.Mesh;
    if (!mesh.isMesh) return;
    mesh.geometry.dispose();
    const materials = Array.isArray(mesh.material) ? mesh.material : [mesh.material];
    for (const material of materials) {
      for (const value of Object.values(material)) if (value instanceof THREE.Texture) value.dispose();
      material.dispose();
    }
  });
}

export class Stage {
  readonly params: StageParams;
  readonly speech: Speech;
  // Mouth shapes of the lines queued in speech, when the service sent them.
  readonly lips = new LipTrack();
  readonly bubble?: Bubble;
  private readonly search: URLSearchParams;
  private readonly renderer: THREE.WebGLRenderer;
  private readonly scene = new THREE.Scene();
  private readonly camera = new THREE.PerspectiveCamera(30, 1, 0.05, 50);
  private readonly controls: OrbitControls;
  private readonly timer = new THREE.Timer();
  private readonly anchor = new THREE.Vector3();
  private outline?: OutlineEffect;
  private _character?: Character;
  private bodyHeight = DEFAULT_BODY_HEIGHT;
  private raf = 0;
  private disposed = false;
  private loading?: Promise<Character>;

  // bubbleEl: the element the speech bubble lives in (needs a `.bubble-text` child); optional.
  constructor(
    canvas: HTMLCanvasElement,
    search: URLSearchParams,
    bubbleEl?: HTMLElement,
  ) {
    this.search = search;
    this.params = parseStageParams(search);
    this.renderer = new THREE.WebGLRenderer({ canvas, antialias: true, alpha: true });
    this.renderer.setPixelRatio(Math.min(devicePixelRatio, 2));
    this.renderer.outputColorSpace = THREE.SRGBColorSpace;

    this.scene.add(new THREE.HemisphereLight(0xfff4e6, 0x3a3050, 1.6));
    const sun = new THREE.DirectionalLight(0xffffff, 2.2);
    sun.position.set(1.5, 3, 4);
    this.scene.add(sun);

    this.controls = new OrbitControls(this.camera, canvas);
    this.controls.enableDamping = true;
    this.controls.enablePan = false;

    this.speech = new Speech();
    if (bubbleEl) this.bubble = new Bubble(bubbleEl);
    window.addEventListener("resize", this.resize);
    this.resize();
  }

  // Undefined until load() resolves.
  // Set by the conversation: nothing is happening, so she may fidget now and then.
  idle = false;

  get character(): Character | undefined {
    return this._character;
  }

  // Starts rendering and loads the character. Safe to call more than once; rejects if the
  // stage is disposed first or the asset can't be loaded.
  load(): Promise<Character> {
    this.loading ??= this.start();
    return this.loading;
  }

  dispose() {
    if (this.disposed) return;
    this.disposed = true;
    cancelAnimationFrame(this.raf);
    window.removeEventListener("resize", this.resize);
    if (import.meta.env.DEV && (window as { hekate?: unknown }).hekate === this._character) {
      delete (window as { hekate?: unknown }).hekate;
    }
    this.speech.dispose();
    this.bubble?.hide();
    this.controls.dispose();
    if (this._character) {
      this.scene.remove(this._character.root);
      disposeObject(this._character.root);
    }
    this.renderer.dispose();
    this.renderer.forceContextLoss();
  }

  private async start(): Promise<Character> {
    // load() can be called after dispose() (React StrictMode cleans up the first effect before a
    // deferred load runs); never start a frame loop on a disposed stage.
    this.assertAlive();
    this.raf = requestAnimationFrame(this.loop);
    const source = await loadCharacterSource(this.search);
    this.assertAlive();
    const character = await Character.load(source.modelUrl, source.rig);
    if (this.disposed) {
      disposeObject(character.root);
      this.assertAlive();
    }
    void warmCharacterCache(["/character/manifest.json", source.modelUrl]);
    this._character = character;
    this.scene.add(character.root);
    this.outline = new OutlineEffect(this.renderer);
    if (import.meta.env.DEV) Object.assign(window, { hekate: character });
    if (!this.params.blink) character.blinkEnabled = false;
    this.bodyHeight = character.bounds().getSize(new THREE.Vector3()).y;
    this.frame();
    if (this.params.gesture) character.playGesture(this.params.gesture);
    return character;
  }

  private assertAlive() {
    if (this.disposed) throw new DOMException("stage disposed", "AbortError");
  }

  private frame() {
    const { camera, controls } = this;
    camera.aspect = window.innerWidth / window.innerHeight;
    const face =
      this.params.view === "face"
        ? { headY: this._character?.head.getWorldPosition(new THREE.Vector3()).y }
        : undefined;
    const framing = computeFraming({ aspect: camera.aspect, fovDegrees: camera.fov, bodyHeight: this.bodyHeight, face });
    camera.position.set(...framing.position);
    controls.target.set(...framing.target);
    camera.updateProjectionMatrix();
    controls.update();
  }

  private resize = () => {
    this.renderer.setSize(window.innerWidth, window.innerHeight, false);
    this.frame();
  };

  private loop = (time: number) => {
    if (this.disposed) return;
    this.timer.update(time);
    const dt = Math.min(this.timer.getDelta(), 0.1);
    const character = this._character;
    if (character) {
      const target = mouthTarget(this.speech.level(), this.params.forcedMouth);
      character.mouthOpen = smoothMouth(character.mouthOpen, target, dt);
      character.visemes = this.params.forcedMouth === undefined ? this.lips.at(this.speech.position) : undefined;
      character.speaking = this.speech.speaking || this.params.forcedMouth !== undefined;
      character.fidgets = this.idle && !this.params.gesture;
      character.update(dt);
      for (const m of this.params.pinnedMorphs) character.setMorph(m.name, m.weight);
      this.bubble?.update(character.headScreenAnchor(this.camera, this.anchor));
    }
    this.controls.update();
    (this.outline ?? this.renderer).render(this.scene, this.camera);
    this.raf = requestAnimationFrame(this.loop);
  };
}
