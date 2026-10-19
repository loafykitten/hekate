import { defineConfig } from "vitest/config";

// Separate from vite.config.ts so tests don't load the PWA and dev-server plugins.
export default defineConfig({
  test: { include: ["src/**/*.test.ts"], environment: "node" },
});
