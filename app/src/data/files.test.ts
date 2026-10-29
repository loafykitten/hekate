import { afterEach, describe, expect, it, vi } from "vitest";
import { asThreadFile } from "./model";
import { formatSize, MAX_FILE_BYTES, splitName, uploadMessage, uploadThreadFile } from "./files";

const raw = { id: "f_1", thread_id: "th_1", turn_id: "", direction: "in", name: "backup.zip", size: 84_000_000, path: "/srv/files/in/f_1/backup.zip", created_at: "2026-10-28T10:00:00Z" };

describe("asThreadFile", () => {
  it("reads a file in either direction", () => {
    expect(asThreadFile(raw)).toEqual({ id: "f_1", thread_id: "th_1", turn_id: "", direction: "in", name: "backup.zip", size: 84_000_000, created_at: "2026-10-28T10:00:00Z" });
    expect(asThreadFile({ ...raw, direction: "out", turn_id: "t_9" })).toMatchObject({ direction: "out", turn_id: "t_9" });
  });
  it("refuses what isn't a file, and tolerates a bad size", () => {
    expect(asThreadFile({ ...raw, direction: "sideways" })).toBeUndefined();
    expect(asThreadFile({ ...raw, name: 4 })).toBeUndefined();
    expect(asThreadFile(null)).toBeUndefined();
    expect(asThreadFile({ ...raw, size: "big" })?.size).toBe(0);
  });
});

describe("formatSize", () => {
  it("picks a readable unit", () => {
    expect(formatSize(0)).toBe("0 B");
    expect(formatSize(512)).toBe("512 B");
    expect(formatSize(1500)).toBe("1.5 KB");
    expect(formatSize(84_000_000)).toBe("84 MB");
    expect(formatSize(1_000_000)).toBe("1 MB");
    expect(formatSize(999_999)).toBe("1 MB");
    expect(formatSize(2 * 1024 ** 3)).toBe("2.1 GB");
    expect(formatSize(-1)).toBe("");
  });
});

describe("splitName", () => {
  it("keeps the extension apart from the stem", () => {
    expect(splitName("ps5-homebrew.zip")).toEqual(["ps5-homebrew", ".zip"]);
    expect(splitName("archive.tar.gz")).toEqual(["archive.tar", ".gz"]);
    expect(splitName(".bashrc")).toEqual([".bashrc", ""]);
    expect(splitName("README")).toEqual(["README", ""]);
    expect(splitName("notes.a-very-long-suffix")).toEqual(["notes.a-very-long-suffix", ""]);
  });
});

describe("uploadMessage", () => {
  it("explains the statuses in the owner's terms", () => {
    expect(uploadMessage(413)).toMatch(/too big/);
    expect(uploadMessage(507)).toMatch(/disk space/);
    expect(uploadMessage(503, "No lab is set up.")).toBe("No lab is set up.");
    expect(uploadMessage(409)).toMatch(/archived/);
    expect(uploadMessage(500)).toBe("Hekate answered 500.");
  });
});

class FakeXhr {
  static last: FakeXhr;
  upload: { onprogress?: (e: { lengthComputable: boolean; loaded: number; total: number }) => void } = {};
  onload?: () => void;
  onerror?: () => void;
  onabort?: () => void;
  status = 0;
  responseText = "";
  method = "";
  url = "";
  sent?: unknown;
  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader() {}
  send(body: unknown) {
    this.sent = body;
  }
  abort() {
    this.onabort?.();
  }
  reply(status: number, body: unknown) {
    this.status = status;
    this.responseText = typeof body === "string" ? body : JSON.stringify(body);
    this.onload?.();
  }
}

describe("uploadThreadFile", () => {
  afterEach(() => vi.unstubAllGlobals());
  const stub = () => vi.stubGlobal("XMLHttpRequest", FakeXhr);
  const file = (name: string, size = 10) => ({ name, size }) as File;

  it("posts the bytes with the encoded name, reports progress, and returns the file", async () => {
    stub();
    const progress: number[] = [];
    const f = file("my backup (1).zip");
    const up = uploadThreadFile("th 1", f, (p) => progress.push(p));
    expect(FakeXhr.last.method).toBe("POST");
    expect(FakeXhr.last.url).toBe("/api/threads/th%201/files?name=my%20backup%20(1).zip");
    expect(FakeXhr.last.sent).toBe(f);
    FakeXhr.last.upload.onprogress?.({ lengthComputable: true, loaded: 5, total: 10 });
    FakeXhr.last.reply(201, raw);
    expect(progress).toEqual([0.5]);
    await expect(up.promise).resolves.toMatchObject({ id: "f_1" });
  });

  it("turns the service's error into readable text", async () => {
    stub();
    const up = uploadThreadFile("th_1", file("a.zip"), () => {});
    FakeXhr.last.reply(503, { error: "Her lab isn't set up." });
    await expect(up.promise).rejects.toMatchObject({ message: "Her lab isn't set up.", status: 503 });
    const big = uploadThreadFile("th_1", file("a.zip"), () => {});
    FakeXhr.last.reply(413, { error: "file too large" });
    await expect(big.promise).rejects.toMatchObject({ message: expect.stringMatching(/too big/), status: 413 });
    const gw = uploadThreadFile("th_1", file("a.zip"), () => {});
    FakeXhr.last.reply(502, "<html>Bad gateway</html>");
    await expect(gw.promise).rejects.toMatchObject({ status: 0 });
  });

  it("refuses over 2 GiB without sending, and aborts on request", async () => {
    stub();
    const huge = uploadThreadFile("th_1", file("huge.bin", MAX_FILE_BYTES + 1), () => {});
    await expect(huge.promise).rejects.toMatchObject({ status: 413 });
    const up = uploadThreadFile("th_1", file("a.zip"), () => {});
    up.abort();
    await expect(up.promise).rejects.toMatchObject({ name: "AbortError" });
  });
});
