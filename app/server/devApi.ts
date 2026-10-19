// Dev-only API for the slice: serves the character asset and synthesizes speech.
// This stands in for the eventual homelab service.
import { spawn } from "node:child_process";
import { createReadStream, existsSync } from "node:fs";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { Plugin } from "vite";

// The asset pipeline's output: manifest.json plus the GLBs it lists.
const assetDir = path.resolve(import.meta.dirname, "../../assets/renamon-nukorio");

const MAX_TEXT = 1000;
const TTS_TIMEOUT_MS = 20_000;
let ttsBusy = false;

// /character/<file>: plain file names inside the asset folder only.
function resolveAssetFile(url: string): string | undefined {
  const m = /^\/character\/([\w.-]+\.(?:glb|json))$/.exec(url);
  if (!m) return undefined;
  const file = path.join(assetDir, m[1]);
  return existsSync(file) ? file : undefined;
}

async function readBody(req: IncomingMessage): Promise<string> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 16_384) throw new Error("body too large");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

function run(cmd: string, args: string[], stdin: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { stdio: ["pipe", "ignore", "pipe"], timeout: TTS_TIMEOUT_MS });
    let stderr = "";
    child.stderr.on("data", (d) => (stderr += d));
    child.on("error", reject);
    child.on("close", (code, signal) =>
      code === 0 ? resolve() : reject(new Error(`${cmd} exited ${code ?? signal}: ${stderr}`)),
    );
    child.stdin.end(stdin);
  });
}

// macOS `say` for now; the real service will swap in a proper TTS engine.
async function synthesize(text: string): Promise<Buffer> {
  const dir = await mkdtemp(path.join(tmpdir(), "hekate-tts-"));
  try {
    const out = path.join(dir, "speech.wav");
    await run("say", ["-o", out, "--file-format=WAVE", "--data-format=LEI16@22050", "-f", "-"], text);
    return await readFile(out);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

async function handleTts(req: IncomingMessage, res: ServerResponse) {
  if (req.method !== "POST") {
    res.statusCode = 405;
    return res.end();
  }
  let text: unknown;
  try {
    text = JSON.parse(await readBody(req)).text;
  } catch {
    res.statusCode = 400;
    return res.end("invalid JSON");
  }
  if (typeof text !== "string" || !text.trim() || text.length > MAX_TEXT) {
    res.statusCode = 400;
    return res.end(`text must be 1-${MAX_TEXT} characters`);
  }
  // One synthesis at a time is plenty for a single user and keeps `say` from piling up.
  if (ttsBusy) {
    res.statusCode = 429;
    return res.end("busy");
  }
  ttsBusy = true;
  try {
    const wav = await synthesize(text);
    res.setHeader("Content-Type", "audio/wav");
    res.end(wav);
  } catch (err) {
    res.statusCode = 500;
    res.end(String(err));
  } finally {
    ttsBusy = false;
  }
}

function sendFile(res: ServerResponse, file: string, type: string) {
  res.setHeader("Content-Type", type);
  res.setHeader("Cache-Control", "no-store");
  createReadStream(file).pipe(res);
}

export function devApi(): Plugin {
  return {
    name: "hekate-dev-api",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url?.split("?")[0];
        if (url === "/api/tts") {
          handleTts(req, res).catch(next);
          return;
        }
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
