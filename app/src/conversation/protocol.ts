// Message shapes of the conversation socket (GET /api/ws). docs/PROTOCOL.md is the contract;
// keep this file in step with it.

export interface Capabilities {
  stt: boolean;
  tts: boolean;
}

export interface Ready {
  type: "ready";
  client_id: string;
  conversation_id: string;
  capabilities: Capabilities;
  gestures: string[];
  timezone?: string; // IANA zone the service schedules in
}

export interface Transcript {
  type: "transcript";
  id: string;
  text: string;
  superseded?: boolean; // newer input arrived meanwhile; no turn follows
}

export interface TurnStart {
  type: "turn_start";
  turn_id: string;
  input_id: string;
  origin: string;
  user_text: string;
}

export interface Activity {
  type: "activity";
  turn_id: string;
  tool: string;
  label: string;
  state: "start" | "done" | "error";
}

export interface SegmentWord {
  text: string;
  start: number;
  end: number;
}

export interface SegmentGesture {
  name: string;
  at: number;
}

export interface SegmentExpression {
  name: string;
  at: number;
}

export interface Segment {
  type: "segment";
  turn_id: string;
  seq: number;
  text: string;
  duration: number;
  audio_b64: string | null;
  audio_format: string | null;
  words: SegmentWord[] | null;
  gestures: SegmentGesture[];
  expressions: SegmentExpression[];
}

export interface Card {
  type: "card";
  turn_id: string;
  card: { kind: string; id: string; title: string; data: unknown };
}

export interface TurnEnd {
  type: "turn_end";
  turn_id: string;
  status: "done" | "interrupted" | "error";
  text: string;
  model: string;
  usage: { input_tokens: number; output_tokens: number; cost_usd: number | null };
}

export interface ServerError {
  type: "error";
  code: string;
  message: string;
  input_id?: string;
}

// What the service can do changed after connecting (e.g. speech-to-text finished loading).
export interface CapabilitiesChanged {
  type: "capabilities";
  capabilities: Capabilities;
}

// Something fired with no turn running (a reminder, an approval she asks for, a change that
// finished). `card` has the same shape as a turn's card. `kind` stays a string so a newer
// service's kinds still show as a plain banner.
export type NotifyKind = "reminder" | "approval" | "change";
export interface Notify {
  type: "notify";
  id: string;
  kind: NotifyKind | (string & {});
  title: string;
  body: string;
  card?: unknown;
}

// Data changed (by REST or by her tools): refetch those sections of the menu.
export interface Invalidate {
  type: "invalidate";
  domains: string[];
}

// An approval was created or changed (decided, ran, expired): clients update any card showing it.
// `approval` is read with the guards in data/model.
export interface ApprovalChanged {
  type: "approval";
  approval: unknown;
}

export type ServerMessage =
  | Ready
  | CapabilitiesChanged
  | Transcript
  | TurnStart
  | Activity
  | Segment
  | Card
  | TurnEnd
  | ServerError
  | Notify
  | Invalidate
  | ApprovalChanged;
export type ServerMessageType = ServerMessage["type"];

export type ClientMessage =
  | { type: "say"; id: string; text: string }
  | { type: "ptt_start"; id: string; sample_rate: number }
  | { type: "ptt_end"; id: string }
  | { type: "ptt_cancel"; id: string }
  | { type: "interrupt"; heard_seq: number; turn_id?: string };

export const MAX_TEXT_LENGTH = 4000;
export const PTT_SAMPLE_RATE = 16000;

export interface HistoryMessage {
  id: string;
  role: "user" | "assistant";
  text: string;
  turn_id?: string;
  created_at: string;
}

const SERVER_TYPES: ReadonlySet<string> = new Set<ServerMessageType>([
  "ready",
  "capabilities",
  "transcript",
  "turn_start",
  "activity",
  "segment",
  "card",
  "turn_end",
  "error",
  "notify",
  "invalidate",
  "approval",
]);

// Parses one text frame. Returns undefined for anything that isn't a known server message, so
// a newer service adding message types never breaks an older client.
export function parseServerMessage(data: unknown): ServerMessage | undefined {
  if (typeof data !== "string") return undefined;
  let value: unknown;
  try {
    value = JSON.parse(data);
  } catch {
    return undefined;
  }
  if (typeof value !== "object" || value === null) return undefined;
  const type = (value as { type?: unknown }).type;
  if (typeof type !== "string" || !SERVER_TYPES.has(type)) return undefined;
  return value as ServerMessage;
}

export function newId(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}
