// The parts of the service worker that are plain logic, kept apart so they can be tested.

export interface PushPayload {
  title: string;
  body: string;
  url: string;
  tag?: string;
}

const FALLBACK: PushPayload = { title: "Hekate", body: "", url: "/" };

// Reads a push message: the service sends JSON `{ title, body, url, tag }`. Anything else
// (empty, plain text, broken JSON) still produces a notification rather than silence.
export function parsePushPayload(text: string | undefined): PushPayload {
  if (!text) return FALLBACK;
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return { ...FALLBACK, body: text.slice(0, 300) };
  }
  if (typeof value !== "object" || value === null) return FALLBACK;
  const v = value as Record<string, unknown>;
  return {
    title: typeof v.title === "string" && v.title ? v.title : FALLBACK.title,
    body: typeof v.body === "string" ? v.body : "",
    url: typeof v.url === "string" && v.url ? v.url : "/",
    tag: typeof v.tag === "string" && v.tag ? v.tag : undefined,
  };
}

// Where a notification click goes: the notification's own url, but only within this app.
export function clickTarget(url: unknown, origin: string): string {
  if (typeof url !== "string") return "/";
  try {
    const target = new URL(url, origin);
    return target.origin === origin ? `${target.pathname}${target.search}${target.hash}` : "/";
  } catch {
    return "/";
  }
}

// The character model and its manifest load from cache on revisits and refresh in the background.
export function isCharacterAsset(pathname: string): boolean {
  return pathname === "/character/manifest.json" || /^\/character\/[\w.-]+\.glb$/.test(pathname);
}
