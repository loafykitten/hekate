import * as THREE from "three";
import { GLTFLoader } from "three/addons/loaders/GLTFLoader.js";
import { TailMotion } from "./tail";

export interface MorphWeight {
  name: string;
  weight: number;
}

export interface OutlineStyle {
  color: [number, number, number]; // linear RGB
  thickness: number; // OutlineEffect units
}

// What the app needs to know about a character beyond the GLB itself, from the asset
// pipeline's manifest.json.
export interface CharacterRig {
  headBone: string;
  // Morph targets blended in proportion to how open the mouth is.
  mouth: MorphWeight[];
  // Eyelid morph targets, closed at weight 1.
  blink: string[];
  // Outlines are drawn by OutlineEffect, styled per mesh (null = no outline; fallback covers
  // unlisted meshes).
  outline: { byMesh: Record<string, OutlineStyle | null>; fallback: OutlineStyle | null };
  // Looping idle, a looping clip to play while speaking, one-shot gestures, quiet one-shots for
  // idle moments (fidgets), and which clips move the head enough that the procedural head layer
  // should back off.
  clips: { idle?: string; talk?: string; gestures: string[]; fidgets: string[]; headDriven: string[] };
  // chinLift (radians): how far to raise the head while speaking, for idles that look down.
  procedural: { spine?: string; tail: string[]; ears: string[]; chinLift: number };
}

const toonRamp = (() => {
  const data = new Uint8Array([90, 170, 255]);
  const tex = new THREE.DataTexture(data, data.length, 1, THREE.RedFormat);
  tex.minFilter = tex.magFilter = THREE.NearestFilter;
  tex.needsUpdate = true;
  return tex;
})();

function toonify(mesh: THREE.Mesh, outline: CharacterRig["outline"]) {
  const src = mesh.material as THREE.MeshStandardMaterial;
  // Unlit materials (KHR_materials_unlit, e.g. painted eyes) arrive as MeshBasicMaterial and stay unlit.
  const material: THREE.MeshToonMaterial | THREE.MeshBasicMaterial = (src as THREE.Material as THREE.MeshBasicMaterial)
    .isMeshBasicMaterial
    ? (src as THREE.Material as THREE.MeshBasicMaterial).clone()
    : new THREE.MeshToonMaterial({
        name: src.name,
        color: src.color,
        map: src.map,
        emissive: src.emissive,
        emissiveMap: src.emissiveMap,
        gradientMap: toonRamp,
        transparent: src.transparent,
        alphaTest: src.alphaTest,
        depthWrite: src.depthWrite,
        side: src.side,
      });
  // Each mesh gets its own material instance, so meshes sharing a texture can still differ in outline.
  const style = mesh.name in outline.byMesh ? outline.byMesh[mesh.name] : outline.fallback;
  material.userData.outlineParameters = style
    ? { color: style.color, thickness: style.thickness, visible: true }
    : { visible: false };
  mesh.material = material;
  if (src.transparent) mesh.renderOrder = 1;
}

const _q = new THREE.Quaternion();
const _dq = new THREE.Quaternion();
const _qi = new THREE.Quaternion();

// Crossfade lengths (seconds), eased. Gestures start and end in a different stance than a calm
// idle (e.g. hands clasped vs. at the sides), so these are long enough to read as a deliberate
// move rather than a snap. The return from a gesture starts this long before it ends, while the
// clip is already settling.
const GESTURE_IN = 0.5;
const GESTURE_OUT = 0.6;
const LOOP_FADE = 0.7;
// Seconds of quiet between idle variations.
const FIDGET_GAP_MIN = 25;
const FIDGET_GAP_MAX = 60;

type Pose = Map<string, THREE.Quaternion>;

// Mean rotation difference (radians) over the bones both poses share.
function poseDistance(a: Pose, b: Pose) {
  let sum = 0;
  let n = 0;
  for (const [bone, q] of a) {
    const r = b.get(bone);
    if (r) {
      sum += q.angleTo(r);
      n++;
    }
  }
  return n ? sum / n : Infinity;
}

// Axis in the bone's local frame that matches a world axis at rest, so procedural
// motion reads the same regardless of how the rig's bones are oriented.
function localAxisFor(bone: THREE.Bone, worldAxis: THREE.Vector3): THREE.Vector3 {
  bone.getWorldQuaternion(_q);
  return worldAxis.clone().applyQuaternion(_q.invert()).normalize();
}

interface DrivenBone {
  bone: THREE.Bone;
  // This frame's pose before procedural layers: the clip's output, or rest if no clip drives it.
  base: THREE.Quaternion;
  axes: Record<string, THREE.Vector3>;
}

export class Character {
  readonly root: THREE.Object3D;
  readonly head: THREE.Bone;
  readonly animations: THREE.AnimationClip[];
  private mixer: THREE.AnimationMixer;
  private driven = new Map<string, DrivenBone>();
  // Morph name -> every (mesh, index) carrying it; a pose usually spans head, eyes and lids.
  private morphs = new Map<string, [THREE.Mesh, number][]>();
  private nextBlink = 2;
  private blinkUntil = 0;
  private time = 0;
  mouthOpen = 0;
  speaking = false;
  private engaged = 0; // eases toward 1 while speaking, so the chin lift isn't a snap
  blinkEnabled = true;
  // Face overlay from the conversation (laughs, sighs...): morph weights, plus how far to
  // close the eyes. Blended on top of speech and blinks with max(), so it never fights them.
  private expression: { weights: Record<string, number>; eyesClosed: number } = { weights: {}, eyesClosed: 0 };
  private overlaid = new Set<string>();
  private loopAction?: THREE.AnimationAction; // idle or talk, whichever is the base right now
  // fadeOut: return fade length; the return starts that long before the clip ends.
  private gesture?: { action: THREE.AnimationAction; headDriven: boolean; fadeOut: number; fidget: boolean };
  // Idle variations: while the conversation is idle, now and then a quiet clip (shifting weight,
  // a glance aside) so she doesn't stand frozen. The conversation turns this on and off.
  fidgets = false;
  private nextFidget?: number;
  private lastFidget?: string;
  // Our own crossfader. Each fade snapshots every action's current weight and interpolates the
  // whole set toward the target, so weights always sum to 1 and outgoing actions only decrease.
  // three's crossFadeTo assumes the outgoing action is at full weight, which pops when a fade is
  // interrupted (gesture during gesture, speech flicker).
  private blend = new Map<THREE.AnimationAction, { from: number; weight: number }>();
  private blendTarget?: THREE.AnimationAction;
  private blendSeconds = 0.3;
  private blendProgress = 1;
  // Two actions per gesture clip, so a gesture can restart while its previous run fades out.
  private gestureActions = new Map<string, THREE.AnimationAction[]>();
  private bones = new Map<string, THREE.Bone>();
  private idleName?: string;
  private headLayer = 1; // procedural head motion scale; backs off during nods and shakes
  private readonly tailMotion: TailMotion;
  // The bone the tail hangs from, and its world rotation last frame.
  private readonly tailParent: THREE.Object3D | undefined;
  private readonly tailParentQ = new THREE.Quaternion();
  private tailPrimed = false; // tailParentQ is read on the first frame, once a clip has posed her
  private readonly tailStance = new THREE.Quaternion(); // the tail parent's pose in her frame, first frame

  private constructor(root: THREE.Object3D, animations: THREE.AnimationClip[], readonly rig: CharacterRig) {
    this.root = root;
    this.animations = animations;
    this.mixer = new THREE.AnimationMixer(root);

    const bones = new Map<string, THREE.Bone>();
    root.traverse((o) => {
      if ((o as THREE.Bone).isBone) bones.set(o.name, o as THREE.Bone);
      const mesh = o as THREE.Mesh;
      if (mesh.isMesh) {
        toonify(mesh, rig.outline);
        mesh.frustumCulled = false; // skinned bounds don't follow the animation
        for (const [name, i] of Object.entries(mesh.morphTargetDictionary ?? {})) {
          if (!this.morphs.has(name)) this.morphs.set(name, []);
          this.morphs.get(name)!.push([mesh, i]);
        }
      }
    });
    const head = bones.get(rig.headBone);
    if (!head) throw new Error(`head bone ${rig.headBone} not found`);
    this.head = head;

    root.updateMatrixWorld(true);
    const X = new THREE.Vector3(1, 0, 0);
    const Y = new THREE.Vector3(0, 1, 0);
    const Z = new THREE.Vector3(0, 0, 1);
    const drive = (name: string | undefined) => {
      const bone = name ? bones.get(name) : undefined;
      if (!bone) return;
      this.driven.set(name!, {
        bone,
        base: bone.quaternion.clone(),
        axes: { pitch: localAxisFor(bone, X), yaw: localAxisFor(bone, Y), roll: localAxisFor(bone, Z) },
      });
    };
    const p = rig.procedural;
    [rig.headBone, p.spine, ...p.tail, ...p.ears].forEach(drive);
    this.tailMotion = new TailMotion(p.tail.length);
    this.tailParent = (p.tail[0] && bones.get(p.tail[0])?.parent) || undefined;

    this.bones = bones;
    this.idleName = rig.clips.idle;
    const idle = this.clip(this.idleName);
    if (idle) {
      this.loopAction = this.mixer.clipAction(idle);
      this.loopAction.play();
      this.blend.set(this.loopAction, { from: 1, weight: 1 });
      this.blendTarget = this.loopAction;
    }
    this.mixer.addEventListener("finished", (e) => {
      if (this.gesture && e.action === this.gesture.action) this.endGesture();
    });
  }

  // A character over a scene that's already loaded (tests build tiny ones).
  static fromScene(root: THREE.Object3D, animations: THREE.AnimationClip[], rig: CharacterRig): Character {
    return new Character(root, animations, rig);
  }

  // The one-shot playing now (a gesture or a fidget), if any.
  get playing(): string | undefined {
    return this.gesture?.action.getClip().name;
  }

  static async load(url: string, rig: CharacterRig): Promise<Character> {
    const gltf = await new GLTFLoader().loadAsync(url);
    return new Character(gltf.scene, gltf.animations, rig);
  }

  get idleClip() {
    return this.idleName;
  }

  get gestures() {
    return this.rig.clips.gestures.filter((g) => this.clip(g));
  }

  private clip(name: string | undefined) {
    return name ? this.animations.find((c) => c.name === name) : undefined;
  }

  // The loop that should be underneath right now: talk while speaking (if the asset has one).
  private wantedLoop() {
    const talk = this.speaking ? this.clip(this.rig.clips.talk) : undefined;
    return talk ?? this.clip(this.idleName);
  }

  // A clip's pose at time t, read from its tracks without touching the skeleton. Bones the
  // procedural layers drive are left out so they don't skew comparisons.
  private poseAt(clip: THREE.AnimationClip, t: number): Pose {
    const pose: Pose = new Map();
    for (const track of clip.tracks) {
      if (!track.name.endsWith(".quaternion")) continue;
      const bone = track.name.slice(0, -".quaternion".length);
      if (this.driven.has(bone)) continue;
      // Quaternion tracks override this factory with a slerp interpolant.
      const v = track.InterpolantFactoryMethodLinear(new Float32Array(4)).evaluate(t);
      pose.set(bone, new THREE.Quaternion(v[0], v[1], v[2], v[3]));
    }
    return pose;
  }

  private currentPose(): Pose {
    const pose: Pose = new Map();
    for (const [name, bone] of this.bones) if (!this.driven.has(name)) pose.set(name, bone.quaternion.clone());
    return pose;
  }

  // Time in a loop clip whose pose is closest to `pose`, so entering the loop doesn't swing limbs.
  private closestPhase(clip: THREE.AnimationClip, pose: Pose, samples = 24) {
    let best = 0;
    let bestDist = Infinity;
    for (let i = 0; i < samples; i++) {
      const t = (i / samples) * clip.duration;
      const d = poseDistance(pose, this.poseAt(clip, t));
      if (d < bestDist) [best, bestDist] = [t, d];
    }
    return best;
  }

  // Make `next` the action everything fades toward. An action still blended in keeps its
  // timeline (a loop resumes where it was); one not in the blend starts at `startAt` (default 0).
  private fadeTo(next: THREE.AnimationAction | undefined, seconds: number, startAt = 0) {
    // Snapshot, renormalized in case an entry was just dropped, so the set always sums to 1.
    let total = 0;
    for (const entry of this.blend.values()) total += entry.weight;
    for (const entry of this.blend.values()) entry.from = total > 0 ? entry.weight / total : 0;
    if (next && !this.blend.has(next)) {
      next.reset().play();
      next.time = startAt;
      this.blend.set(next, { from: 0, weight: 0 });
    }
    this.blendTarget = next;
    this.blendSeconds = seconds;
    this.blendProgress = 0;
  }

  private updateBlend(dt: number) {
    this.blendProgress = Math.min(1, this.blendProgress + dt / this.blendSeconds);
    const p = this.blendProgress;
    const s = p * p * (3 - 2 * p); // ease in and out: no velocity kink at either end of the fade
    for (const [action, entry] of this.blend) {
      const isTarget = action === this.blendTarget;
      entry.weight = entry.from * (1 - s) + (isTarget ? s : 0);
      if (p === 1 && !isTarget) {
        action.stop();
        this.blend.delete(action);
        continue;
      }
      action.setEffectiveWeight(entry.weight);
    }
  }

  // An instance of the gesture's clip that isn't still blended in from a previous run.
  private freshGestureAction(clip: THREE.AnimationClip) {
    let pool = this.gestureActions.get(clip.name);
    if (!pool) {
      pool = [this.mixer.clipAction(clip), this.mixer.clipAction(clip.clone())];
      for (const a of pool) {
        a.setLoop(THREE.LoopOnce, 1);
        a.clampWhenFinished = true; // hold the last pose while fading back out
      }
      this.gestureActions.set(clip.name, pool);
    }
    // Both busy only if re-triggered twice within one fade; take the one with less influence.
    return pool.find((a) => !this.blend.has(a)) ?? pool.reduce((a, b) => (this.blend.get(a)!.weight <= this.blend.get(b)!.weight ? a : b));
  }

  // One-shot gesture over the current loop, then back to whichever loop fits by then.
  // Asking for the gesture that's already playing is a no-op rather than a snap back to frame 0.
  playGesture(name: string): boolean {
    return this.playOneShot(name, false);
  }

  private playOneShot(name: string, fidget: boolean): boolean {
    const clip = this.clip(name);
    if (!clip || !(fidget ? this.rig.clips.fidgets : this.rig.clips.gestures).includes(name)) return false;
    if (this.gesture?.action.getClip().name === name) return true;
    const action = this.freshGestureAction(clip);
    if (this.blend.has(action)) this.blend.delete(action); // rare double re-trigger: restart it from zero
    // Short clips get proportionally shorter fades so they still play visibly.
    const fade = (max: number) => Math.min(max, clip.duration / 3);
    this.fadeTo(action, fade(GESTURE_IN));
    this.gesture = { action, headDriven: this.rig.clips.headDriven.includes(name), fadeOut: fade(GESTURE_OUT), fidget };
    return true;
  }

  // Picks the next idle variation once she has been quiet long enough. Anything else happening
  // (speech, a gesture, the conversation leaving idle) restarts the wait; a fidget in progress
  // gives way at once.
  private updateFidgets(t: number) {
    const names = this.rig.clips.fidgets.filter((n) => this.clip(n));
    const quiet = this.fidgets && !this.speaking;
    if (this.gesture?.fidget && !quiet) this.endGesture();
    if (!quiet || this.gesture || names.length === 0) {
      this.nextFidget = undefined;
      return;
    }
    this.nextFidget ??= t + FIDGET_GAP_MIN + Math.random() * (FIDGET_GAP_MAX - FIDGET_GAP_MIN);
    if (t < this.nextFidget) return;
    const choices = names.length > 1 ? names.filter((n) => n !== this.lastFidget) : names;
    const name = choices[Math.floor(Math.random() * choices.length)];
    this.lastFidget = name;
    this.nextFidget = undefined;
    this.playOneShot(name, true);
  }

  private endGesture() {
    const loop = this.wantedLoop();
    this.loopAction = loop ? this.mixer.clipAction(loop) : undefined;
    this.fadeTo(this.loopAction, this.gesture!.fadeOut, loop ? this.closestPhase(loop, this.currentPose()) : 0);
    this.gesture = undefined;
  }

  get morphNames() {
    return [...this.morphs.keys()];
  }

  setMorph(name: string, weight: number) {
    for (const [mesh, i] of this.morphs.get(name) ?? []) mesh.morphTargetInfluences![i] = weight;
  }

  setExpression(weights: Record<string, number>, eyesClosed = 0) {
    this.expression = { weights, eyesClosed };
  }

  private morphWeight(name: string): number {
    const target = this.morphs.get(name)?.[0];
    return target ? (target[0].morphTargetInfluences?.[target[1]] ?? 0) : 0;
  }

  // Applies the expression overlay after the mouth and blink morphs were written this frame.
  private applyExpression() {
    const { weights, eyesClosed } = this.expression;
    const overlay = new Map(Object.entries(weights));
    if (eyesClosed > 0) for (const n of this.rig.blink) overlay.set(n, Math.max(overlay.get(n) ?? 0, eyesClosed));
    // Morphs the mouth and blink code rewrites every frame keep that value as the floor; the
    // others start from zero, so releasing an expression eases back to neutral.
    const driven = new Set([...this.rig.mouth.map((w) => w.name), ...(this.blinkEnabled ? this.rig.blink : [])]);
    for (const name of new Set([...overlay.keys(), ...this.overlaid])) {
      const floor = driven.has(name) ? this.morphWeight(name) : 0;
      this.setMorph(name, Math.max(floor, overlay.get(name) ?? 0));
    }
    this.overlaid = new Set(overlay.keys());
  }

  private layer(name: string | undefined, rotations: [string, number][]) {
    const d = name ? this.driven.get(name) : undefined;
    if (!d) return;
    d.bone.quaternion.copy(d.base);
    for (const [axis, angle] of rotations) {
      if (angle) d.bone.quaternion.multiply(_q.setFromAxisAngle(d.axes[axis], angle));
    }
  }

  update(dt: number) {
    this.time += dt;
    const t = this.time;

    // Switch between idle and talk loops as speech starts and stops (gestures finish first).
    const wanted = this.wantedLoop();
    if (!this.gesture && wanted && this.loopAction?.getClip() !== wanted) {
      const next = this.mixer.clipAction(wanted);
      this.fadeTo(next, LOOP_FADE, this.closestPhase(wanted, this.currentPose()));
      this.loopAction = next;
    }
    // Head back to the loop while the gesture is still settling, not after it has frozen.
    const g = this.gesture;
    if (g && g.action.time >= g.action.getClip().duration - g.fadeOut) this.endGesture();
    this.updateFidgets(t);
    const loopName = this.loopAction?.getClip().name;
    const clipOwnsHead = this.gesture ? this.gesture.headDriven : !!loopName && this.rig.clips.headDriven.includes(loopName);
    const headTarget = clipOwnsHead ? 0.25 : 1;
    this.headLayer += (headTarget - this.headLayer) * (1 - Math.exp(-6 * dt));

    // The mixer only writes values that changed since its last write, so strip last frame's
    // procedural layers first or they'd pile up on bones whose clip value is constant.
    for (const d of this.driven.values()) d.bone.quaternion.copy(d.base);
    this.updateBlend(dt);
    this.mixer.update(dt);
    for (const d of this.driven.values()) d.base.copy(d.bone.quaternion);

    const p = this.rig.procedural;
    const breathe = Math.sin(t * 1.6);
    const talking = this.mouthOpen;

    this.layer(p.spine, [["pitch", 0.025 * breathe]]);
    this.engaged += ((this.speaking ? 1 : 0) - this.engaged) * (1 - Math.exp(-4 * dt));

    // The tail lags behind whatever the body did this frame (the clip, or the stage turning her).
    let baseTurn = 0;
    let baseTip = 0;
    let baseLean = 0;
    if (this.tailParent) {
      this.tailParent.updateWorldMatrix(true, false);
      // Her hips relative to her own frame, against how she first stood.
      this.root.getWorldQuaternion(_dq).invert();
      this.tailParent.getWorldQuaternion(_q).premultiply(_dq);
      if (!this.tailPrimed) this.tailStance.copy(_q);
      _dq.copy(_q).multiply(_qi.copy(this.tailStance).invert());
      baseLean = 2 * Math.sign(_dq.w) * _dq.x;

      this.tailParent.getWorldQuaternion(_q);
      if (this.tailPrimed) {
        _dq.copy(_q).multiply(this.tailParentQ.invert());
        if (_dq.w < 0) _dq.set(-_dq.x, -_dq.y, -_dq.z, -_dq.w); // the short way round
        // Small rotations: the vector part is about half the angle times the axis.
        baseTurn = 2 * _dq.y;
        baseTip = 2 * _dq.x;
      }
      this.tailParentQ.copy(_q);
      this.tailPrimed = true;
    }
    const tail = this.tailMotion.update({ dt, liveliness: this.engaged, baseTurn, baseTip, baseLean });
    p.tail.forEach((n, i) => this.layer(n, [["pitch", tail[i].pitch], ["yaw", tail[i].yaw]]));

    for (const w of this.rig.mouth) this.setMorph(w.name, w.weight * talking);
    // A slight chin lift while speaking reads as engagement.
    this.layer(this.rig.headBone, [["pitch", this.headLayer * (0.03 * Math.sin(t * 0.7) - p.chinLift * this.engaged)]]);
    p.ears.forEach((n, i) => this.layer(n, [["pitch", 0.05 * Math.sin(t * 2 + i) * (0.3 + talking)]]));

    if (this.blinkEnabled) {
      if (t >= this.nextBlink) {
        this.blinkUntil = t + 0.12;
        this.nextBlink = t + 2 + Math.random() * 4;
      }
      for (const n of this.rig.blink) this.setMorph(n, t < this.blinkUntil ? 1 : 0);
    }
    this.applyExpression();
  }

  headScreenAnchor(camera: THREE.Camera, out: THREE.Vector3): THREE.Vector3 {
    this.head.getWorldPosition(out);
    out.y += 0.45;
    return out.project(camera);
  }

  bounds(): THREE.Box3 {
    return new THREE.Box3().setFromObject(this.root);
  }
}
