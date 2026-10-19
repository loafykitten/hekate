/// <reference lib="webworker" />
import { clientsClaim } from "workbox-core";
import { CacheableResponsePlugin } from "workbox-cacheable-response";
import { ExpirationPlugin } from "workbox-expiration";
import { cleanupOutdatedCaches, createHandlerBoundToURL, precacheAndRoute, type PrecacheEntry } from "workbox-precaching";
import { NavigationRoute, registerRoute } from "workbox-routing";
import { StaleWhileRevalidate } from "workbox-strategies";
import { clickTarget, isCharacterAsset, parsePushPayload } from "./swLogic";

// The app's service worker (vite-plugin-pwa injectManifest): caches the app shell and the
// character, and handles web push. Replaces the generated worker, with the same caching rules.

declare const self: ServiceWorkerGlobalScope & { __WB_MANIFEST: (string | PrecacheEntry)[] };

// A new worker takes over at once, as registerType "autoUpdate" always did.
void self.skipWaiting();
clientsClaim();

precacheAndRoute(self.__WB_MANIFEST);
cleanupOutdatedCaches();

// Deep links fall back to the app shell, but never the service or the character files.
registerRoute(
  new NavigationRoute(createHandlerBoundToURL("index.html"), {
    denylist: [/^\/api\//, /^\/healthz/, /^\/character\//],
  }),
);

// The 5-11 MB model loads from cache on revisits and refreshes in the background.
registerRoute(
  ({ url }) => isCharacterAsset(url.pathname),
  new StaleWhileRevalidate({
    cacheName: "hekate-character",
    plugins: [new ExpirationPlugin({ maxEntries: 6 }), new CacheableResponsePlugin({ statuses: [200] })],
  }),
);

self.addEventListener("push", (event) => {
  const payload = parsePushPayload(event.data?.text());
  event.waitUntil(
    self.registration.showNotification(payload.title, {
      body: payload.body,
      tag: payload.tag,
      icon: "/pwa-192x192.png",
      badge: "/pwa-64x64.png",
      data: { url: payload.url },
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = clickTarget((event.notification.data as { url?: unknown } | null)?.url, self.location.origin);
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
      const existing = windows.find((c) => new URL(c.url).origin === self.location.origin);
      if (existing) {
        await existing.focus();
        // The open app switches to the right menu section itself.
        existing.postMessage({ type: "open-url", url: target });
        return;
      }
      await self.clients.openWindow(target);
    })(),
  );
});
