// Web push subscription flow, written against small interfaces so the logic is testable with a
// fake PushManager. The Settings section wires it to the real browser APIs.

export type PushErrorCode = "unsupported" | "denied" | "no-worker";

export class PushError extends Error {
  constructor(
    readonly code: PushErrorCode,
    message: string,
  ) {
    super(message);
    this.name = "PushError";
  }
}

export interface SubscriptionLike {
  endpoint: string;
  options?: { applicationServerKey?: ArrayBuffer | null };
  toJSON(): unknown;
  unsubscribe(): Promise<boolean>;
}

export interface PushManagerLike {
  getSubscription(): Promise<SubscriptionLike | null>;
  subscribe(options: { userVisibleOnly: boolean; applicationServerKey: Uint8Array<ArrayBuffer> }): Promise<SubscriptionLike>;
}

export interface PushDeps {
  permission(): NotificationPermission;
  requestPermission(): Promise<NotificationPermission>;
  // The active service worker's push manager; undefined when no worker is running (dev server).
  pushManager(): Promise<PushManagerLike | undefined>;
  fetchKey(): Promise<string>;
  postSubscription(subscription: unknown): Promise<void>;
  deleteSubscription(endpoint: string): Promise<void>;
}

// The VAPID public key arrives base64url encoded; subscribe() wants bytes.
export function urlBase64ToBytes(base64url: string): Uint8Array<ArrayBuffer> {
  const padded = base64url.replace(/-/g, "+").replace(/_/g, "/").padEnd(Math.ceil(base64url.length / 4) * 4, "=");
  const raw = atob(padded);
  const bytes = new Uint8Array(new ArrayBuffer(raw.length));
  for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
  return bytes;
}

function sameBytes(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a) return false;
  const x = new Uint8Array(a);
  return x.length === b.length && x.every((v, i) => v === b[i]);
}

// Asks for permission, subscribes with the service's key and registers the subscription.
// Call straight from a click: browsers only show the permission prompt for a user gesture.
export async function enablePush(deps: PushDeps): Promise<SubscriptionLike> {
  let permission = deps.permission();
  if (permission === "denied") throw new PushError("denied", "Notifications are blocked for Hekate. Allow them in the browser's site settings.");
  if (permission === "default") permission = await deps.requestPermission();
  if (permission !== "granted") throw new PushError("denied", "Notifications weren't allowed.");

  const manager = await deps.pushManager();
  if (!manager) throw new PushError("no-worker", "Push needs the installed app. It isn't available in this build.");

  const key = urlBase64ToBytes(await deps.fetchKey());
  let subscription = await manager.getSubscription();
  // A subscription made for another server key can never receive our pushes.
  if (subscription && subscription.options?.applicationServerKey && !sameBytes(subscription.options.applicationServerKey, key)) {
    await subscription.unsubscribe();
    subscription = null;
  }
  subscription ??= await manager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
  await deps.postSubscription(subscription.toJSON());
  return subscription;
}

// Unsubscribes this browser, then tells the service. Returns false if there was nothing to remove.
export async function disablePush(deps: PushDeps): Promise<boolean> {
  const manager = await deps.pushManager();
  const subscription = await manager?.getSubscription();
  if (!subscription) return false;
  const endpoint = subscription.endpoint;
  await subscription.unsubscribe();
  await deps.deleteSubscription(endpoint);
  return true;
}

export async function isSubscribed(deps: Pick<PushDeps, "pushManager">): Promise<boolean> {
  const manager = await deps.pushManager();
  return !!(await manager?.getSubscription());
}

export interface PushEnvironment {
  ua: string;
  maxTouchPoints: number;
  platform: string;
  navigatorStandalone: boolean | undefined;
  displayModeStandalone: boolean;
  hasServiceWorker: boolean;
  hasPushManager: boolean;
  hasNotification: boolean;
}

export interface PushSupport {
  ios: boolean;
  standalone: boolean;
  // Browser has the APIs (on iOS they only appear for apps added to the Home Screen).
  supported: boolean;
  // iPhone/iPad in a normal browser tab: push can't work until it's added to the Home Screen.
  needsHomeScreen: boolean;
}

export function pushSupport(env: PushEnvironment): PushSupport {
  // iPadOS reports itself as a Mac with a touch screen.
  const ios = /iPhone|iPad|iPod/.test(env.ua) || (env.platform === "MacIntel" && env.maxTouchPoints > 1);
  const standalone = env.navigatorStandalone === true || env.displayModeStandalone;
  const supported = env.hasServiceWorker && env.hasPushManager && env.hasNotification;
  return { ios, standalone, supported, needsHomeScreen: ios && !standalone };
}

export function currentPushEnvironment(): PushEnvironment {
  return {
    ua: navigator.userAgent,
    maxTouchPoints: navigator.maxTouchPoints,
    platform: navigator.platform,
    navigatorStandalone: (navigator as { standalone?: boolean }).standalone,
    displayModeStandalone: typeof matchMedia === "function" && matchMedia("(display-mode: standalone)").matches,
    hasServiceWorker: "serviceWorker" in navigator,
    hasPushManager: "PushManager" in window,
    hasNotification: "Notification" in window,
  };
}

// The active service worker registration's push manager, without waiting forever when there is
// none (the dev server doesn't register one).
export async function browserPushManager(): Promise<PushManagerLike | undefined> {
  if (!("serviceWorker" in navigator)) return undefined;
  if (!(await navigator.serviceWorker.getRegistration())) return undefined;
  const registration = await Promise.race([
    navigator.serviceWorker.ready,
    new Promise<undefined>((resolve) => setTimeout(() => resolve(undefined), 4000)),
  ]);
  return registration?.pushManager as unknown as PushManagerLike | undefined;
}
