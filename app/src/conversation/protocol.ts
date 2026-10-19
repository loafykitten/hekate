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
}

export interface Transcript {
  type: "transcript";
  id: string;
  text: string;
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

export type ServerMessage =
  | Ready
  | CapabilitiesChanged
  | Transcript
  | TurnStart
  | Activity
  | Segment
  | Card
  | TurnEnd
  | ServerError;
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
