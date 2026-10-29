import { asReview, asThreadFile, type Review, type ThreadFile } from "../data/model";
import type { ThreadEvent, TurnEnd } from "./protocol";

// What a thread's view shows of a turn happening now, built from its wrapped events. Text only:
// thread replies have no audio, so there is nothing to play, reveal word by word, or hush.
// Framework free.

export interface LiveStep {
  tool: string;
  label: string;
  state: "running" | "ok" | "error";
}

export interface LiveTurn {
  id: string;
  // The owner's words for this turn, shown until saved history holds them.
  userText: string;
  // The files the owner sent with it.
  files: ThreadFile[];
  text: string;
  steps: LiveStep[];
  ended: boolean;
  status?: TurnEnd["status"];
}

export interface ThreadLive {
  turn?: LiveTurn;
  // Something that went wrong with the last input or turn, shown inline.
  error?: string;
  // The input the error refused, if it names one: the view can give its text back.
  failedInput?: string;
  // Reviews of her lab work seen this session, newest state of each (by id), oldest first.
  reviews: Review[];
  // Bumps when a turn ends or a review ends, so the view refetches the saved transcript.
  epoch: number;
  // Speech was sent and the transcript hasn't come back yet.
  transcribing: boolean;
}

export const IDLE: ThreadLive = { epoch: 0, transcribing: false, reviews: [] };

// Errors that mean the service never took the input (PROTOCOL: a working thread or too many
// running, an unknown thread, too long). Any other error is a turn that was taken and then failed:
// its words are saved, so they mustn't come back as an unsent draft.
const REFUSALS = new Set(["busy", "bad_message", "too_long"]);
export function refused(code: string | undefined): boolean {
  return code !== undefined && REFUSALS.has(code);
}

export function isRunning(live: ThreadLive): boolean {
  return !!live.turn && !live.turn.ended;
}

// A review of her work is under way: the thread is working and can be stopped, with no turn.
export function reviewRunning(reviews: readonly Review[]): boolean {
  return reviews.some((r) => r.status === "running");
}

// The reviews from the saved transcript and from live events, one per id. Live wins, unless it
// still says running after the saved copy has ended (the end came while the app was away).
export function mergeReviews(saved: readonly Review[], live: readonly Review[]): Review[] {
  const byId = new Map<string, Review>();
  for (const r of saved) byId.set(r.id, r);
  for (const r of live) {
    const old = byId.get(r.id);
    if (!old || r.status !== "running" || old.status === "running") byId.set(r.id, r);
  }
  return [...byId.values()].sort((a, b) => a.started_at.localeCompare(b.started_at) || a.round - b.round);
}

// The step to name while she works: the tool running now, else the latest one.
export function currentStep(turn: LiveTurn): LiveStep | undefined {
  return [...turn.steps].reverse().find((s) => s.state === "running");
}


// The turn_start's files, tolerating a missing or odd list.
export function liveFiles(v: unknown): ThreadFile[] {
  return Array.isArray(v) ? v.flatMap((f) => asThreadFile(f) ?? []) : [];
}

function turnFor(state: ThreadLive, id: string): LiveTurn {
  // Joined mid-turn (the app opened while she was working): there was no turn_start to see.
  return state.turn && state.turn.id === id && !state.turn.ended ? state.turn : { id, userText: "", files: [], text: "", steps: [], ended: false };
}

export function reduceThreadEvent(state: ThreadLive, event: ThreadEvent): ThreadLive {
  switch (event.type) {
    case "turn_start":
      return { ...state, error: undefined, failedInput: undefined, transcribing: false, turn: { id: event.turn_id, userText: event.user_text ?? "", files: liveFiles(event.files), text: "", steps: [], ended: false } };
    case "activity": {
      const turn = turnFor(state, event.turn_id);
      let steps: LiveStep[];
      if (event.state === "start") {
        steps = [...turn.steps, { tool: event.tool, label: event.label, state: "running" }];
      } else {
        const at = turn.steps.findLastIndex((s) => s.tool === event.tool && s.state === "running");
        const done: LiveStep = { tool: event.tool, label: event.label, state: event.state === "error" ? "error" : "ok" };
        steps = at < 0 ? [...turn.steps, done] : turn.steps.map((s, i) => (i === at ? { ...s, label: s.label || event.label, state: done.state } : s));
      }
      return { ...state, turn: { ...turn, steps } };
    }
    case "segment": {
      const turn = turnFor(state, event.turn_id);
      return { ...state, turn: { ...turn, text: turn.text + event.text } };
    }
    case "turn_end": {
      const turn = turnFor(state, event.turn_id);
      const steps = turn.steps.map((s) => (s.state === "running" ? { ...s, state: "ok" as const } : s));
      const failed = event.status === "error" && !state.error;
      return {
        ...state,
        epoch: state.epoch + 1,
        error: failed ? "That didn't finish. You can try again." : state.error,
        turn: { ...turn, steps, text: event.text || turn.text, ended: true, status: event.status },
      };
    }
    case "error":
      return {
        ...state,
        error: event.message || "Something went wrong.",
        failedInput: refused(event.code) ? event.input_id : undefined,
        transcribing: false,
      };
    case "review": {
      const review = asReview(event.review);
      if (!review) return state;
      const at = state.reviews.findIndex((r) => r.id === review.id);
      const reviews = at < 0 ? [...state.reviews, review] : state.reviews.map((r, i) => (i === at ? review : r));
      // An ended review changes the thread (running, her follow-up turn): refetch it.
      return { ...state, reviews, epoch: review.status === "running" ? state.epoch : state.epoch + 1 };
    }
    case "card":
      return state; // cards belong to the voice stage; a thread's reply is its text
  }
}

// One entry per thread that has had any event or input this session.
export class ThreadLiveStore {
  private threads = new Map<string, ThreadLive>();
  private listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  };

  get = (thread: string): ThreadLive => this.threads.get(thread) ?? IDLE;

  apply(thread: string, event: ThreadEvent) {
    this.set(thread, reduceThreadEvent(this.get(thread), event));
  }

  // The owner sent something: whatever went wrong before is old news.
  sent(thread: string, transcribing = false) {
    this.set(thread, { ...this.get(thread), error: undefined, failedInput: undefined, transcribing });
  }

  fail(thread: string, code: string, message: string, input?: string) {
    this.set(thread, { ...this.get(thread), error: message, failedInput: refused(code) ? input : undefined, transcribing: false });
  }

  // The saved transcript now holds a finished turn: stop showing it twice.
  settle(thread: string, turnId: string) {
    const live = this.get(thread);
    if (live.turn?.id === turnId && live.turn.ended) this.set(thread, { ...live, turn: undefined });
  }

  clear() {
    this.threads.clear();
    this.emit();
  }

  private set(thread: string, next: ThreadLive) {
    this.threads.set(thread, next);
    this.emit();
  }

  private emit() {
    for (const l of [...this.listeners]) l();
  }
}

// The list shows a thread as running from what this device has seen of it, else from the server.
export function threadRunning(serverRunning: boolean, live: ThreadLive): boolean {
  if (reviewRunning(live.reviews)) return true;
  return live.turn ? !live.turn.ended : serverRunning;
}

// Whether the saved transcript now holds a finished turn's reply, so the live copy can go. A turn
// that wrote nothing has no reply to wait for: its saved words are enough.
export function historyHolds(messages: readonly { role: string; turn_id: string }[], turn: LiveTurn): boolean {
  if (!turn.ended) return false;
  return messages.some((m) => m.turn_id === turn.id && (m.role === "assistant" || !turn.text.trim()));
}

// Where each review stands in the transcript. A review follows the last saved message of the turn
// it reviewed (her reply). If that turn isn't on screen, it stands before the first message of the follow-up
// turn (which has no owner message: the card is where it would be). Else it is `loose`: the
// reviewed turn, or the follow-up, is still only live, so the view puts it beside the live turn.
// Each review lands in exactly one place.
export interface ReviewPlacement {
  after: Map<string, Review[]>; // message id -> reviews that follow it
  before: Map<string, Review[]>; // message id -> reviews that precede it
  loose: Review[];
}

export function placeReviews(messages: readonly { id: string; role: string; turn_id: string }[], reviews: readonly Review[]): ReviewPlacement {
  const placement: ReviewPlacement = { after: new Map(), before: new Map(), loose: [] };
  const push = (map: Map<string, Review[]>, id: string, r: Review) => map.set(id, [...(map.get(id) ?? []), r]);
  for (const r of reviews) {
    // Her reply; or, for a turn that saved none, its last message unless that ends the transcript
    // (then her reply is likely still being written, live).
    const reply = messages.findLast((m) => m.turn_id === r.turn_id && m.role === "assistant");
    const last = messages.findLastIndex((m) => m.turn_id === r.turn_id);
    const reviewed = reply ?? (last >= 0 && last < messages.length - 1 ? messages[last] : undefined);
    const followup = r.followup_turn_id ? messages.find((m) => m.turn_id === r.followup_turn_id) : undefined;
    if (reviewed) push(placement.after, reviewed.id, r);
    else if (followup) push(placement.before, followup.id, r);
    else placement.loose.push(r);
  }
  return placement;
}
