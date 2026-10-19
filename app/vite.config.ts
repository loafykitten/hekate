import { readFileSync } from "node:fs";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";
import { VitePWA } from "vite-plugin-pwa";
import { devAssets } from "./server/devAssets.ts";

// Brand colors come from the design tokens so the manifest (and the theme-color the PWA plugin
// derives from it) never drift.
const tokens = readFileSync(new URL("./src/ui/tokens.css", import.meta.url), "utf8");
function token(name: string): string {
  const value = new RegExp(`--${name}:\\s*(#[0-9a-fA-F]{6})`).exec(tokens)?.[1];
  if (!value) throw new Error(`design token --${name} not found in src/ui/tokens.css`);
  return value;
}
const themeColor = token("bg-top");
const backgroundColor = token("bg-bottom");

// The Go service (built separately) serves the API and, in production, this app and the assets.
const service = "http://127.0.0.1:3200";

export default defineConfig({
  plugins: [
    react(),
    devAssets(),
    VitePWA({
      registerType: "autoUpdate",
      strategies: "generateSW",
      pwaAssets: { config: "pwa-assets.config.ts", overrideManifestIcons: true },
      manifest: {
        name: "Hekate",
        short_name: "Hekate",
        description: "A personal assistant with a face",
        display: "standalone",
        start_url: "/",
        scope: "/",
        theme_color: themeColor,
        background_color: backgroundColor,
      },
      workbox: {
        cleanupOutdatedCaches: true,
        clientsClaim: true,
        // The plugin adds manifest.webmanifest itself; globbing it too creates a conflicting entry.
        globPatterns: ["**/*.{js,css,html,svg,png,ico}"],
        // Deep links fall back to the app shell, but never the service or the character files.
        navigateFallback: "index.html",
        navigateFallbackDenylist: [/^\/api\//, /^\/healthz/, /^\/character\//],
        runtimeCaching: [
          {
            // The 5-11 MB model loads from cache on revisits and refreshes in the background.
            urlPattern: ({ url }) => url.pathname === "/character/manifest.json" || /^\/character\/[\w.-]+\.glb$/.test(url.pathname),
            handler: "StaleWhileRevalidate",
            options: {
              cacheName: "hekate-character",
              expiration: { maxEntries: 6 },
              cacheableResponse: { statuses: [200] },
            },
          },
        ],
      },
    }),
  ],
  // three.js alone is ~700 kB minified; one app chunk is fine for a single-user PWA behind a service worker.
  build: { chunkSizeWarningLimit: 1100 },
  server: {
    // Loopback only by default; `HEKATE_LAN=1 npm run dev` to try it from a phone on the LAN.
    host: process.env.HEKATE_LAN === "1",
    proxy: {
      // Vite logs a proxy error on each poll while the service is down; that is expected in dev.
      "/api": { target: service, ws: true },
      "/healthz": { target: service },
    },
  },
});
