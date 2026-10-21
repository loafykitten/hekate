// Plaid Link, loaded on demand. Link is Plaid's own script (the bank's login happens inside it);
// the app only gets a public token back, which the service exchanges. Nothing here is loaded until
// the owner connects or reconnects a bank.

export const PLAID_SCRIPT = "https://cdn.plaid.com/link/v2/stable/link-initialize.js";
export const OAUTH_PATH = "/money/oauth";

// The small part of window.Plaid this app uses.
export interface PlaidMetadata {
  institution?: { institution_id?: string; name?: string } | null;
  accounts?: { name?: string; mask?: string | null; type?: string; subtype?: string | null }[];
}
export interface PlaidError {
  error_code?: string;
  error_message?: string;
  display_message?: string | null;
}
interface PlaidHandler {
  open(): void;
  destroy(): void;
}
interface PlaidConfig {
  token: string;
  receivedRedirectUri?: string;
  onSuccess(publicToken: string, metadata: PlaidMetadata): void;
  onExit(error: PlaidError | null, metadata: unknown): void;
}
declare global {
  interface Window {
    Plaid?: { create(config: PlaidConfig): PlaidHandler };
  }
}

let loading: Promise<void> | undefined;

// One script tag, reused. A failed load can be tried again.
export function loadPlaid(): Promise<void> {
  if (window.Plaid) return Promise.resolve();
  loading ??= new Promise<void>((resolve, reject) => {
    const fail = () => {
      loading = undefined;
      document.querySelector(`script[src="${PLAID_SCRIPT}"]`)?.remove();
      reject(new Error("Couldn't load Plaid. Check the connection and try again."));
    };
    const script = document.createElement("script");
    script.src = PLAID_SCRIPT;
    script.async = true;
    script.onload = () => (window.Plaid ? resolve() : fail());
    script.onerror = fail;
    document.head.append(script);
  });
  return loading;
}

export interface LinkCallbacks {
  onSuccess(publicToken: string, metadata: PlaidMetadata): void;
  onExit(error: PlaidError | null): void;
}

export async function openLink(token: string, callbacks: LinkCallbacks, receivedRedirectUri?: string): Promise<void> {
  await loadPlaid();
  const handler = window.Plaid!.create({
    token,
    receivedRedirectUri,
    onSuccess: (publicToken, metadata) => {
      callbacks.onSuccess(publicToken, metadata);
      handler.destroy();
    },
    onExit: (error) => {
      callbacks.onExit(error);
      handler.destroy();
    },
  });
  handler.open();
}

// What Link said went wrong, in words fit to show.
export function linkErrorText(error: PlaidError | null): string {
  if (!error) return "";
  return error.display_message || error.error_message || "The bank connection didn't finish.";
}

// --- the OAuth hop ---------------------------------------------------------------------------

// A bank that links through its own site sends the browser away and back to /money/oauth. The link
// token has to survive that trip, so it waits in localStorage (and is removed when used).
const STORE_KEY = "hekate-plaid-link";
const STORE_MAX_AGE = 30 * 60_000;

export interface StoredLink {
  token: string;
  itemId?: string; // set when this is a reconnect
  at: number;
}

export function storeLink(token: string, itemId?: string, now = Date.now(), storage: () => Storage = () => localStorage): void {
  try {
    const link: StoredLink = { token, itemId, at: now };
    storage().setItem(STORE_KEY, JSON.stringify(link));
  } catch {
    // No storage: a normal (popup) flow still works; only the bank-site hop can't resume.
  }
}

export function readLink(now = Date.now(), storage: () => Storage = () => localStorage): StoredLink | undefined {
  try {
    const raw = storage().getItem(STORE_KEY);
    if (!raw) return undefined;
    const v: unknown = JSON.parse(raw);
    if (typeof v !== "object" || v === null) return undefined;
    const r = v as Record<string, unknown>;
    if (typeof r.token !== "string" || r.token === "" || typeof r.at !== "number") return undefined;
    if (now - r.at > STORE_MAX_AGE || r.at > now + 60_000) return undefined;
    return { token: r.token, itemId: typeof r.itemId === "string" && r.itemId !== "" ? r.itemId : undefined, at: r.at };
  } catch {
    return undefined;
  }
}

export function clearLink(storage: () => Storage = () => localStorage): void {
  try {
    storage().removeItem(STORE_KEY);
  } catch {
    // nothing to clear
  }
}

// On load: is this the bank sending the browser back, and is there a token to resume with?
export type OAuthResume = { kind: "none" } | { kind: "resume"; link: StoredLink } | { kind: "lost" };

export function oauthResume(pathname: string, link: StoredLink | undefined): OAuthResume {
  if (pathname.replace(/\/+$/, "") !== OAUTH_PATH) return { kind: "none" };
  return link ? { kind: "resume", link } : { kind: "lost" };
}

// An account as Link listed it, sent along so the service can tell a bank that's already
// connected before the link is exchanged.
export interface LinkedAccount {
  name: string;
  mask: string;
  type: string;
  subtype: string;
}

export function linkedAccounts(metadata: PlaidMetadata): LinkedAccount[] {
  return (metadata.accounts ?? []).map((a) => ({ name: a.name ?? "", mask: a.mask ?? "", type: a.type ?? "", subtype: a.subtype ?? "" }));
}
