import {
  newId,
  parseServerMessage,
  PTT_SAMPLE_RATE,
  type Capabilities,
  type ClientMessage,
  type Ready,
  type ServerMessage,
  type ServerMessageType,
} from "./protocol";

// The conversation socket (GET /api/ws): typed messages in both directions, reconnect with
// exponential backoff, and the "new input interrupts the current turn" rule. Framework-free.

export type ConnectionStatus = "connecting" | "open" | "closed";

export interface ReconnectPolicy {
  baseMs: number;
  maxMs: number;
  factor: number;
}

export const DEFAULT_RECONNECT: ReconnectPolicy = { baseMs: 500, maxMs: 15_000, factor: 2 };

// Delay before reconnect attempt number `attempt` (0 = first retry): exponential, capped, with
// equal jitter (half fixed, half random) so many tabs and devices don't retry in lockstep.
export function backoffDelay(attempt: number, policy: ReconnectPolicy = DEFAULT_RECONNECT, random = Math.random): number {
  const ceiling = Math.min(policy.maxMs, policy.baseMs * policy.factor ** attempt);
  return ceiling / 2 + random() * (ceiling / 2);
}

// The parts of WebSocket the client uses, so tests can pass a fake.
export interface SocketLike {
  readyState: number;
  binaryType: string;
  onopen: ((ev: unknown) => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: unknown) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send(data: string | ArrayBuffer | ArrayBufferView): void;
  close(): void;
}
export type SocketFactory = (url: string) => SocketLike;

const OPEN = 1;

export function defaultSocketUrl(loc: Pick<Location, "protocol" | "host"> = location): string {
  return `${loc.protocol === "https:" ? "wss" : "ws"}://${loc.host}/api/ws`;
}

export interface ClientOptions {
  url?: string;
  socketFactory?: SocketFactory;
  reconnect?: ReconnectPolicy;
  random?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (handle: unknown) => void;
}

type Handler<T> = (message: T) => void;
type MessageOf<K extends ServerMessageType> = Extract<ServerMessage, { type: K }>;

export class ConversationClient {
  private readonly url: string;
  private readonly makeSocket: SocketFactory;
  private readonly policy: ReconnectPolicy;
  private readonly random: () => number;
  private readonly setTimer: (fn: () => void, ms: number) => unknown;
  private readonly clearTimer: (handle: unknown) => void;
  private socket?: SocketLike;
  private retryTimer?: unknown;
  private attempt = 0;
  private stopped = true;
  private _status: ConnectionStatus = "closed";
  private _ready?: Ready;
  private activeTurn?: string;
  private activeOrigin?: string;
  private handlers = new Map<string, Set<Handler<never>>>();
  private statusHandlers = new Set<(status: ConnectionStatus) => void>();

  // The seq of the last segment of the current turn the user heard at least partly, or -1.
  // The playback layer owns that fact; the client only needs it to interrupt on new input.
  getHeardSeq: () => number = () => -1;
  // The turn getHeardSeq describes (the last one this client played).
  getHeardTurn: () => string | undefined = () => undefined;

  constructor(options: ClientOptions = {}) {
    this.url = options.url ?? defaultSocketUrl();
    this.makeSocket = options.socketFactory ?? ((url) => new WebSocket(url) as unknown as SocketLike);
    this.policy = options.reconnect ?? DEFAULT_RECONNECT;
    this.random = options.random ?? Math.random;
    this.setTimer = options.setTimer ?? ((fn, ms) => setTimeout(fn, ms));
    this.clearTimer = options.clearTimer ?? ((h) => clearTimeout(h as ReturnType<typeof setTimeout>));
  }

  get status(): ConnectionStatus {
    return this._status;
  }

  // Set once `ready` has arrived on the current connection.
  get ready(): Ready | undefined {
    return this._ready;
  }

  get clientId(): string | undefined {
    return this._ready?.client_id;
  }

  get capabilities(): Capabilities | undefined {
    return this._ready?.capabilities;
  }

  // True from turn_start until turn_end (or until we interrupt it).
  get turnActive(): boolean {
    return this.activeTurn !== undefined;
  }

  get activeTurnId(): string | undefined {
    return this.activeTurn;
  }

  // The running turn was started by this client (so this client knows what was heard of it).
  get activeTurnMine(): boolean {
    return this.activeTurn !== undefined && this.activeOrigin === this.clientId;
  }

  on<K extends ServerMessageType>(type: K, handler: Handler<MessageOf<K>>): () => void {
    let set = this.handlers.get(type);
    if (!set) this.handlers.set(type, (set = new Set()));
    set.add(handler as Handler<never>);
    return () => set.delete(handler as Handler<never>);
  }

  onStatus(handler: (status: ConnectionStatus) => void): () => void {
    this.statusHandlers.add(handler);
    return () => this.statusHandlers.delete(handler);
  }

  connect() {
    this.stopped = false;
    if (this.socket || this.retryTimer !== undefined) return;
    this.open();
  }

  // Stops reconnecting and closes the socket.
  disconnect() {
    this.stopped = true;
    this.cancelRetry();
    const socket = this.socket;
    this.socket = undefined;
    if (socket) {
      detach(socket);
      socket.close();
    }
    this.resetConnectionState();
    this.setStatus("closed");
  }

  // Skips the remaining backoff (the page came back to the foreground, the network returned).
  reconnectNow() {
    if (this.stopped || this.socket) return;
    this.cancelRetry();
    this.open();
  }

  // `thread` sends the input to that thread. A thread has its own turn: the main conversation's
  // is left running, and the server restarts the thread's own on new input.
  say(text: string, quiet = false, thread?: string): string | undefined {
    if (!this.canSend()) return undefined;
    if (!thread) this.interruptForNewInput();
    const id = newId();
    return this.send({ type: "say", id, text, ...(quiet ? { quiet } : {}), ...(thread ? { thread } : {}) }) ? id : undefined;
  }

  checkIn(day?: string, quiet = false): string | undefined {
    if (!this.canSend()) return undefined;
    this.interruptForNewInput();
    const id = newId();
    return this.send({ type: "checkin", id, ...(day ? { day } : {}), ...(quiet ? { quiet } : {}) }) ? id : undefined;
  }

  pttStart(sampleRate = PTT_SAMPLE_RATE, thread?: string): string | undefined {
    if (!this.canSend()) return undefined;
    if (!thread) this.interruptForNewInput();
    const id = newId();
    return this.send({ type: "ptt_start", id, sample_rate: sampleRate, ...(thread ? { thread } : {}) }) ? id : undefined;
  }

  // One binary frame of little-endian signed 16-bit mono PCM.
  pttAudio(chunk: ArrayBuffer | ArrayBufferView): boolean {
    if (!this.socket || this.socket.readyState !== OPEN) return false;
    this.socket.send(chunk);
    return true;
  }

  pttEnd(id: string, quiet = false, thread?: string): boolean {
    return this.send({ type: "ptt_end", id, ...(quiet ? { quiet } : {}), ...(thread ? { thread } : {}) });
  }

  pttCancel(id: string, thread?: string): boolean {
    return this.send({ type: "ptt_cancel", id, ...(thread ? { thread } : {}) });
  }

  // Stops a thread's turn. Nothing is heard of a thread's reply, so heard_seq is -1; the main
  // conversation's turn bookkeeping is untouched.
  interruptThread(thread: string, turnId?: string): boolean {
    return this.send({ type: "interrupt", heard_seq: -1, thread, ...(turnId ? { turn_id: turnId } : {}) });
  }

  // Stops the current turn; the server keeps segments up to heardSeq in history.
  interrupt(heardSeq: number = this.getHeardSeq(), turnId: string | undefined = this.getHeardTurn() ?? this.activeTurn): boolean {
    this.activeTurn = undefined;
    // Naming the turn lets the server ignore an interrupt that arrives after a newer turn began.
    return this.send(turnId ? { type: "interrupt", heard_seq: heardSeq, turn_id: turnId } : { type: "interrupt", heard_seq: heardSeq });
  }

  private canSend(): boolean {
    return !!this._ready && !!this.socket && this.socket.readyState === OPEN;
  }

  // New input while our own turn is running stops it first, reporting what was heard. Another
  // device's turn is left to the server, which interrupts it on new input keeping what it
  // delivered; we don't know what was heard there.
  private interruptForNewInput() {
    if (this.activeTurnMine) this.interrupt();
  }

  private send(message: ClientMessage): boolean {
    if (!this.socket || this.socket.readyState !== OPEN) return false;
    this.socket.send(JSON.stringify(message));
    return true;
  }

  private open() {
    this.setStatus("connecting");
    let socket: SocketLike;
    try {
      socket = this.makeSocket(this.url);
    } catch {
      this.scheduleRetry();
      return;
    }
    socket.binaryType = "arraybuffer";
    this.socket = socket;
    socket.onmessage = (ev) => this.handleFrame(ev.data);
    socket.onclose = () => {
      if (this.socket !== socket) return;
      detach(socket);
      this.socket = undefined;
      this.resetConnectionState();
      this.setStatus("closed");
      this.scheduleRetry();
    };
    // A failed connection also fires close, which does the work.
    socket.onerror = () => {};
  }

  private handleFrame(data: unknown) {
    const message = parseServerMessage(data);
    if (!message) return;
    switch (message.type) {
      case "ready":
        this._ready = message;
        this.attempt = 0;
        this.setStatus("open");
        break;
      case "turn_start":
        this.activeTurn = message.turn_id;
        this.activeOrigin = message.origin;
        break;
      case "turn_end":
        if (this.activeTurn === message.turn_id) this.activeTurn = undefined;
        break;
    }
    const set = this.handlers.get(message.type);
    if (!set) return;
    for (const handler of [...set]) (handler as Handler<ServerMessage>)(message);
  }

  private scheduleRetry() {
    if (this.stopped) return;
    const delay = backoffDelay(this.attempt++, this.policy, this.random);
    this.retryTimer = this.setTimer(() => {
      this.retryTimer = undefined;
      if (!this.stopped && !this.socket) this.open();
    }, delay);
  }

  private cancelRetry() {
    if (this.retryTimer === undefined) return;
    this.clearTimer(this.retryTimer);
    this.retryTimer = undefined;
  }

  private resetConnectionState() {
    this._ready = undefined;
    this.activeTurn = undefined;
  }

  private setStatus(status: ConnectionStatus) {
    if (this._status === status) return;
    this._status = status;
    for (const handler of [...this.statusHandlers]) handler(status);
  }
}

function detach(socket: SocketLike) {
  socket.onopen = socket.onmessage = socket.onclose = socket.onerror = null;
}
