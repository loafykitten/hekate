// Procedural tail: a resting curve plus layered motion, as per-bone angles (radians) the
// Character applies on top of the clips (no clip keys the tail).
//
// - Carriage: the tail droops from its base and levels out toward the tip, instead of the
//   rig's straight rest pose, and drifts slowly from one side to the other.
// - Sway and lift: slow travelling waves, larger toward the tip, built from a few incommensurate
//   frequencies so they never visibly repeat. Their phases are integrated, so speeding up while
//   she talks never makes the tail jump.
// - Follow-through: each bone has a damped spring. When the body turns or tips, the tail lags
//   and then settles; now and then a small flick is kicked into the tip.

export interface TailAngles {
  pitch: number; // + lifts toward the sky
  yaw: number; // + swings toward her right
}

export interface TailInput {
  dt: number; // seconds since the last update
  // 0 calm .. 1 animated (she's talking).
  liveliness: number;
  // How far the tail's base turned (about the vertical) and tipped since the last update, in
  // radians. The springs push the other way, so the tail lags behind the body.
  baseTurn: number;
  baseTip: number;
}

const deg = Math.PI / 180;

// Resting curve, as each bone's pitch along the tail (0 = base, 1 = tip), for an 8-bone tail:
// a 17° droop at the base easing into a 17° lift at the tip, so the tip clears the floor. Scaled for other bone counts so the
// overall shape holds.
const DROOP_BASE = -17 * deg;
const DROOP_TIP = 17 * deg;
const REFERENCE_BONES = 8;

// Whole-tail amplitudes (radians, summed over the bones).
const SWAY = 0.95; // side to side
const LIFT = 0.3; // up and down
const DRIFT = 0.4; // the slow lean to one side
// Base angular frequencies (rad/s) and how much talking speeds them up.
const SWAY_RATE = 1.2;
const LIFT_RATE = 0.75;
const LIVELY_RATE = 0.3;
const LIVELY_AMP = 0.35;
// Wave travel along the tail: the tip trails the base by this much phase.
const SWAY_LAG = 1.8;
const LIFT_LAG = 1.2;

// Springs: stiffness (1/s²) and damping (1/s); underdamped, so a kick overshoots once and settles.
const STIFFNESS = 38;
const DAMPING = 4.2;
const MAX_OFFSET = 0.2; // per bone; offsets add up along the tail
// How much of the body's motion each bone resists, base to tip.
const DRAG_BASE = 0.15;
const DRAG_TIP = 0.9;
// Flicks: seconds between them, and the kick (rad/s) at the tip.
const FLICK_MIN = 7;
const FLICK_MAX = 18;
const FLICK_KICK = 0.8; // about 20° at the tip, summed along the tail

// A few incommensurate sines: smooth, bounded to about ±1, and never visibly periodic.
function wobble(phase: number): number {
  return 0.62 * Math.sin(phase) + 0.26 * Math.sin(phase * 1.618 + 1.3) + 0.12 * Math.sin(phase * 2.71 + 4.1);
}

interface Spring {
  pitch: number;
  yaw: number;
  vPitch: number;
  vYaw: number;
}

export class TailMotion {
  private swayPhase = 0;
  private liftPhase = 0;
  private driftPhase: number;
  private lively = 0;
  private readonly springs: Spring[];
  private nextFlick: number;
  private time = 0;
  private readonly out: TailAngles[];

  constructor(
    readonly bones: number,
    private readonly random: () => number = Math.random,
  ) {
    this.springs = Array.from({ length: bones }, () => ({ pitch: 0, yaw: 0, vPitch: 0, vYaw: 0 }));
    this.out = Array.from({ length: bones }, () => ({ pitch: 0, yaw: 0 }));
    this.driftPhase = random() * Math.PI * 2;
    this.nextFlick = this.flickGap();
  }

  private flickGap(): number {
    return FLICK_MIN + this.random() * (FLICK_MAX - FLICK_MIN);
  }

  // Advances the motion and returns each bone's angles, base first. The array is reused.
  update({ dt, liveliness, baseTurn, baseTip }: TailInput): readonly TailAngles[] {
    const n = this.bones;
    if (n === 0) return this.out;
    dt = Math.min(Math.max(dt, 0), 0.1); // a stalled tab must not fling the springs
    this.time += dt;

    // Ease toward the target liveliness so a reply starting doesn't snap the rhythm.
    this.lively += (Math.min(Math.max(liveliness, 0), 1) - this.lively) * (1 - Math.exp(-2.5 * dt));
    const rate = 1 + LIVELY_RATE * this.lively;
    const amp = 1 + LIVELY_AMP * this.lively;
    this.swayPhase += SWAY_RATE * rate * dt;
    this.liftPhase += LIFT_RATE * rate * dt;
    this.driftPhase += 0.12 * dt; // about a minute from one side to the other and back

    // An occasional flick toward a random side, into the outer half of the tail.
    let flick = 0;
    if (this.time >= this.nextFlick) {
      flick = (this.random() < 0.5 ? -1 : 1) * FLICK_KICK * amp;
      this.nextFlick = this.time + this.flickGap();
    }

    const scale = REFERENCE_BONES / n; // per-bone share of a whole-tail angle, relative to 8 bones
    const drift = (DRIFT / n) * Math.sin(this.driftPhase);
    for (let i = 0; i < n; i++) {
      const s = n === 1 ? 1 : i / (n - 1); // 0 at the base, 1 at the tip
      const reach = 0.45 + 0.55 * s; // the tip moves more than the base
      const sp = this.springs[i];

      const drag = DRAG_BASE + (DRAG_TIP - DRAG_BASE) * s;
      sp.vYaw -= drag * baseTurn * STIFFNESS * 0.12;
      sp.vPitch -= drag * baseTip * STIFFNESS * 0.12;
      if (flick && s >= 0.5) sp.vYaw += flick * s;
      sp.vYaw += (-STIFFNESS * sp.yaw - DAMPING * sp.vYaw) * dt;
      sp.vPitch += (-STIFFNESS * sp.pitch - DAMPING * sp.vPitch) * dt;
      sp.yaw += sp.vYaw * dt;
      sp.pitch += sp.vPitch * dt;
      if (Math.abs(sp.yaw) > MAX_OFFSET) [sp.yaw, sp.vYaw] = [Math.sign(sp.yaw) * MAX_OFFSET, 0];
      if (Math.abs(sp.pitch) > MAX_OFFSET) [sp.pitch, sp.vPitch] = [Math.sign(sp.pitch) * MAX_OFFSET, 0];

      const rest = (DROOP_BASE + (DROOP_TIP - DROOP_BASE) * s) * (n === 1 ? 0 : scale);
      const sway = (SWAY / n) * amp * reach * wobble(this.swayPhase - i * (SWAY_LAG / n));
      const lift = (LIFT / n) * amp * reach * wobble(this.liftPhase - i * (LIFT_LAG / n) + 2);
      this.out[i].pitch = rest + lift + sp.pitch;
      this.out[i].yaw = drift + sway + sp.yaw;
    }
    return this.out;
  }
}
