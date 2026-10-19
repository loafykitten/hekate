import { defineConfig } from "vite";
import { devApi } from "./server/devApi";

export default defineConfig({
  plugins: [devApi()],
  // Loopback only by default; `HEKATE_LAN=1 npm run dev` to try it from a phone on the LAN.
  server: { host: process.env.HEKATE_LAN === "1" },
});
