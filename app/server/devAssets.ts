// Dev-only: serves the (git-ignored) character asset folder at /character/<file>, the same path
// the Go service serves it from in production. Manifest and GLBs only.
import { createReadStream, existsSync } from "node:fs";
import path from "node:path";
import type { ServerResponse } from "node:http";
import type { Plugin } from "vite";

// The asset pipeline's output: manifest.json plus the GLBs it lists.
const assetDir = path.resolve(import.meta.dirname, "../../assets/renamon-nukorio");

// /character/<file>: plain file names inside the asset folder only.
function resolveAssetFile(url: string): string | undefined {
  const m = /^\/character\/([\w.-]+\.(?:glb|json))$/.exec(url);
  if (!m) return undefined;
  const file = path.join(assetDir, m[1]);
  return existsSync(file) ? file : undefined;
}

function sendFile(res: ServerResponse, file: string, type: string) {
  res.setHeader("Content-Type", type);
  res.setHeader("Cache-Control", "no-store");
  createReadStream(file).pipe(res);
}

export function devAssets(): Plugin {
  return {
    name: "hekate-dev-assets",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url?.split("?")[0];
        if (url?.startsWith("/character/")) {
          const file = resolveAssetFile(url);
          if (!file) {
            res.statusCode = 404;
            return res.end();
          }
          return sendFile(res, file, file.endsWith(".glb") ? "model/gltf-binary" : "application/json");
        }
        next();
      });
    },
  };
}
