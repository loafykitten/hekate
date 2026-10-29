import { asThreadFile, isRec, type ThreadFile } from "./model";

// Files sent to her in a thread (PROTOCOL: Threads, Files). Framework free.

export const MAX_FILE_BYTES = 2 * 1024 ** 3;
export const MAX_ATTACHMENTS = 10;

// "84 MB", "1.5 GB", "512 B": decimal units, one decimal only when it helps.
export function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1000) return `${Math.round(bytes)} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let value = bytes;
  let i = -1;
  do {
    value /= 1000;
    i++;
  } while (value >= 999.5 && i < units.length - 1);
  const text = value < 10 ? value.toFixed(1).replace(/\.0$/, "") : String(Math.round(value));
  return `${text} ${units[i]}`;
}

export function fileUrl(thread: string, file: string): string {
  return `/api/threads/${encodeURIComponent(thread)}/files/${encodeURIComponent(file)}`;
}

export class UploadError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
    this.name = "UploadError";
  }
}

// What the owner reads when an upload fails, from the status and the service's own text.
export function uploadMessage(status: number, own?: string): string {
  switch (status) {
    case 0:
      return "Hekate isn't reachable. Check your connection and try again.";
    case 409:
      return "This thread is archived. Restore it to send files.";
    case 413:
      return "That file is too big (2 GB at most).";
    case 503:
      return own || "Her lab isn't set up, so she can't take files yet.";
    case 507:
      return "Her lab is nearly out of disk space.";
    default:
      return own || `Hekate answered ${status}.`;
  }
}

export interface Upload {
  promise: Promise<ThreadFile>;
  abort: () => void;
}

// POSTs the file's bytes to the thread. XMLHttpRequest, because fetch can't report upload progress.
export function uploadThreadFile(thread: string, file: File, onProgress: (fraction: number) => void): Upload {
  const xhr = new XMLHttpRequest();
  const promise = new Promise<ThreadFile>((resolve, reject) => {
    if (file.size > MAX_FILE_BYTES) {
      reject(new UploadError(uploadMessage(413), 413));
      return;
    }
    xhr.open("POST", `/api/threads/${encodeURIComponent(thread)}/files?name=${encodeURIComponent(file.name)}`);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(Math.min(1, e.loaded / e.total));
    };
    xhr.onerror = () => reject(new UploadError(uploadMessage(0), 0));
    xhr.onabort = () => reject(new DOMException("Upload cancelled", "AbortError"));
    xhr.onload = () => {
      let body: unknown;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        body = undefined;
      }
      const own = isRec(body) && typeof body.error === "string" && body.error ? body.error : undefined;
      if (xhr.status >= 200 && xhr.status < 300) {
        const parsed = asThreadFile(body);
        if (parsed) resolve(parsed);
        else reject(new UploadError("Hekate sent something unexpected.", xhr.status));
        return;
      }
      // A gateway answer without the service's own text means it is out of reach.
      const gateway = [502, 503, 504].includes(xhr.status) && !own;
      reject(new UploadError(uploadMessage(gateway ? 0 : xhr.status, own), gateway ? 0 : xhr.status));
    };
    xhr.send(file);
  });
  return { promise, abort: () => xhr.abort() };
}

// A file name as [stem, extension], so a long name can ellipsize its stem and keep ".zip" visible.
export function splitName(name: string): [string, string] {
  const dot = name.lastIndexOf(".");
  if (dot <= 0 || name.length - dot > 9) return [name, ""];
  return [name.slice(0, dot), name.slice(dot)];
}
