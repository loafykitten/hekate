import { describe, expect, it, vi } from "vitest";
import {
  disablePush,
  enablePush,
  isSubscribed,
  PushError,
  pushSupport,
  urlBase64ToBytes,
  type PushDeps,
  type PushEnvironment,
  type PushManagerLike,
  type SubscriptionLike,
} from "./push";

const KEY_BYTES = [4, 250, 17, 255, 0, 63];
const KEY = btoa(String.fromCharCode(...KEY_BYTES)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

class FakeSubscription implements SubscriptionLike {
  unsubscribed = false;
  constructor(
    readonly endpoint: string,
    readonly options?: { applicationServerKey?: ArrayBuffer | null },
  ) {}
  toJSON() {
    return { endpoint: this.endpoint, keys: { p256dh: "p", auth: "a" } };
  }
  async unsubscribe() {
    this.unsubscribed = true;
    return true;
  }
}

class FakePushManager implements PushManagerLike {
  subscribeCalls: { userVisibleOnly: boolean; applicationServerKey: Uint8Array }[] = [];
  constructor(public current: FakeSubscription | null = null) {}
  async getSubscription() {
    return this.current && !this.current.unsubscribed ? this.current : null;
  }
  async subscribe(options: { userVisibleOnly: boolean; applicationServerKey: Uint8Array<ArrayBuffer> }) {
    this.subscribeCalls.push(options);
    this.current = new FakeSubscription("https://push.example/new", { applicationServerKey: options.applicationServerKey.buffer });
    return this.current;
  }
}

function deps(over: Partial<PushDeps> & { manager?: FakePushManager | undefined } = {}) {
  const manager = "manager" in over ? over.manager : new FakePushManager();
  const calls: string[] = [];
  const d: PushDeps = {
    permission: () => "default",
    requestPermission: vi.fn(async () => "granted" as NotificationPermission),
    pushManager: async () => manager,
    fetchKey: vi.fn(async () => {
      calls.push("key");
      return KEY;
    }),
    postSubscription: vi.fn(async () => void calls.push("post")),
    deleteSubscription: vi.fn(async () => void calls.push("delete")),
    ...over,
  };
  return { d, manager, calls };
}

describe("urlBase64ToBytes", () => {
  it("decodes base64url with or without padding", () => {
    expect([...urlBase64ToBytes(KEY)]).toEqual(KEY_BYTES);
    expect([...urlBase64ToBytes("-_8")]).toEqual([251, 255]);
  });
});

describe("enablePush", () => {
  it("asks for permission, subscribes with the service's key and posts the subscription", async () => {
    const { d, manager, calls } = deps();
    const sub = await enablePush(d);
    expect(d.requestPermission).toHaveBeenCalledOnce();
    expect(manager!.subscribeCalls).toHaveLength(1);
    expect(manager!.subscribeCalls[0].userVisibleOnly).toBe(true);
    expect([...manager!.subscribeCalls[0].applicationServerKey]).toEqual(KEY_BYTES);
    expect(d.postSubscription).toHaveBeenCalledWith(sub.toJSON());
    expect(calls).toEqual(["key", "post"]);
  });

  it("asks for permission before anything else (it needs the click)", async () => {
    const order: string[] = [];
    const { d } = deps({
      requestPermission: async () => (order.push("permission"), "granted"),
      fetchKey: async () => (order.push("key"), KEY),
    });
    await enablePush(d);
    expect(order).toEqual(["permission", "key"]);
  });

  it("skips the prompt when permission is already granted", async () => {
    const { d } = deps({ permission: () => "granted" });
    await enablePush(d);
    expect(d.requestPermission).not.toHaveBeenCalled();
  });

  it("fails with a clear error when permission is blocked or refused", async () => {
    const blocked = deps({ permission: () => "denied" });
    await expect(enablePush(blocked.d)).rejects.toMatchObject({ code: "denied" });
    expect(blocked.d.requestPermission).not.toHaveBeenCalled();
    const refused = deps({ requestPermission: async () => "denied" });
    await expect(enablePush(refused.d)).rejects.toBeInstanceOf(PushError);
    expect(refused.manager!.subscribeCalls).toHaveLength(0);
    expect(refused.d.postSubscription).not.toHaveBeenCalled();
  });

  it("fails when no service worker is active", async () => {
    const { d } = deps({ manager: undefined });
    await expect(enablePush(d)).rejects.toMatchObject({ code: "no-worker" });
  });

  it("reuses an existing subscription made with the same key and registers it again", async () => {
    const existing = new FakeSubscription("https://push.example/old", { applicationServerKey: urlBase64ToBytes(KEY).buffer });
    const { d, manager } = deps({ manager: new FakePushManager(existing) });
    const sub = await enablePush(d);
    expect(sub).toBe(existing);
    expect(manager!.subscribeCalls).toHaveLength(0);
    expect(d.postSubscription).toHaveBeenCalledWith(existing.toJSON());
  });

  it("replaces a subscription made for a different server key", async () => {
    const existing = new FakeSubscription("https://push.example/old", { applicationServerKey: new Uint8Array([1, 2, 3]).buffer });
    const { d, manager } = deps({ manager: new FakePushManager(existing) });
    const sub = await enablePush(d);
    expect(existing.unsubscribed).toBe(true);
    expect(sub.endpoint).toBe("https://push.example/new");
    expect(manager!.subscribeCalls).toHaveLength(1);
  });

  it("does not claim success when the service refuses the subscription", async () => {
    const { d } = deps({ postSubscription: async () => Promise.reject(new Error("push is not configured")) });
    await expect(enablePush(d)).rejects.toThrow("push is not configured");
  });
});

describe("disablePush", () => {
  it("unsubscribes the browser and tells the service", async () => {
    const existing = new FakeSubscription("https://push.example/old");
    const { d } = deps({ manager: new FakePushManager(existing) });
    expect(await disablePush(d)).toBe(true);
    expect(existing.unsubscribed).toBe(true);
    expect(d.deleteSubscription).toHaveBeenCalledWith("https://push.example/old");
  });

  it("does nothing without a subscription", async () => {
    const { d } = deps();
    expect(await disablePush(d)).toBe(false);
    expect(d.deleteSubscription).not.toHaveBeenCalled();
  });

  it("reports whether this browser is subscribed", async () => {
    expect(await isSubscribed(deps().d)).toBe(false);
    expect(await isSubscribed(deps({ manager: new FakePushManager(new FakeSubscription("e")) }).d)).toBe(true);
    expect(await isSubscribed(deps({ manager: undefined }).d)).toBe(false);
  });
});

describe("pushSupport", () => {
  const env = (over: Partial<PushEnvironment>): PushEnvironment => ({
    ua: "Mozilla/5.0 (X11; Linux x86_64) Chrome/130",
    maxTouchPoints: 0,
    platform: "Linux x86_64",
    navigatorStandalone: undefined,
    displayModeStandalone: false,
    hasServiceWorker: true,
    hasPushManager: true,
    hasNotification: true,
    ...over,
  });
  const iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 19_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148";

  it("works in a desktop browser", () => {
    expect(pushSupport(env({}))).toEqual({ ios: false, standalone: false, supported: true, needsHomeScreen: false });
  });

  it("asks iPhone users to add the app to the Home Screen first", () => {
    expect(pushSupport(env({ ua: iphone, hasPushManager: false, hasNotification: false }))).toMatchObject({ ios: true, standalone: false, needsHomeScreen: true });
  });

  it("is fine on an iPhone once it's a Home Screen app", () => {
    expect(pushSupport(env({ ua: iphone, navigatorStandalone: true }))).toMatchObject({ ios: true, standalone: true, supported: true, needsHomeScreen: false });
    expect(pushSupport(env({ ua: iphone, displayModeStandalone: true }))).toMatchObject({ standalone: true, needsHomeScreen: false });
  });

  it("recognises an iPad that reports itself as a Mac", () => {
    expect(pushSupport(env({ ua: "Mozilla/5.0 (Macintosh)", platform: "MacIntel", maxTouchPoints: 5 })).ios).toBe(true);
    expect(pushSupport(env({ ua: "Mozilla/5.0 (Macintosh)", platform: "MacIntel", maxTouchPoints: 0 })).ios).toBe(false);
  });

  it("reports a browser without the APIs as unsupported", () => {
    expect(pushSupport(env({ hasPushManager: false })).supported).toBe(false);
  });
});
