// Generates the PWA icon set (favicon.ico, 64/192/512 PNGs, maskable, apple-touch) from the
// original mark in public/favicon.svg. Run by vite-plugin-pwa at build time; nothing is committed.
import { defineConfig, minimal2023Preset } from "@vite-pwa/assets-generator/config";

const background = "#2b2440"; // --bg-top in src/ui/tokens.css

export default defineConfig({
  preset: {
    ...minimal2023Preset,
    // The mark already has a full-bleed background and keeps clear of the edges, so it works
    // as-is for maskable and apple-touch icons without extra padding.
    maskable: { sizes: [512], padding: 0, resizeOptions: { background, fit: "contain" } },
    apple: { sizes: [180], padding: 0, resizeOptions: { background, fit: "contain" } },
    transparent: { ...minimal2023Preset.transparent, padding: 0 },
  },
  images: ["public/favicon.svg"],
});
