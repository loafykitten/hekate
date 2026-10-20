import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
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

// Buster for the persisted menu cache (src/data/persist.ts): a hash of the data layer's source, so
// a change to what the queries return or how it is parsed never meets data stored by an older build.
const dataDir = new URL("./src/data/", import.meta.url);
const buildId = createHash("sha256")
  .update(
    readdirSync(dataDir)
      .filter((f) => f.endsWith(".ts") && !f.endsWith(".test.ts"))
      .sort()
      .map((f) => readFileSync(new URL(f, dataDir), "utf8"))
      .join("\0"),
  )
  .digest("hex")
  .slice(0, 12);

// The Go service (built separately) serves the API and, in production, this app and the assets.
const service = "http://127.0.0.1:3200";

export default defineConfig({
  define: { __BUILD_ID__: JSON.stringify(buildId) },
  plugins: [
    react(),
    devAssets(),
    VitePWA({
      registerType: "autoUpdate",
      strategies: "injectManifest",
      srcDir: "src",
      filename: "sw.ts",
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
      // The worker lives in src/sw.ts: precaching and runtime caching as before, plus web push.
      injectManifest: {
        // The plugin adds the web manifest to the precache itself. Globbing it here too would list
        // it twice with different revisions, and workbox-precaching throws on that at startup.
        globPatterns: ["**/*.{js,css,html,svg,png,ico}"],
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
