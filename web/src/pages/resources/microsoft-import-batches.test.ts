import { describe, expect, it, vi } from "vitest";
import { IamApiError } from "@/lib/api-client";

import { createMicrosoftImportBatches, runMicrosoftImportBatches } from "./microsoft-import-batches";

describe("Microsoft import batches", () => {
  it("keeps 100 MB intact and splits larger input into at most 99 MB per request", () => {
    const line = "x".repeat(999_999) + "\n";
    const content = line.repeat(100);
    expect(createMicrosoftImportBatches(content).chunks.length).toBe(1);
    const larger = content + "last line";
    const { chunks } = createMicrosoftImportBatches(larger);
    expect(chunks.map((chunk) => new Blob([chunk]).size)).toEqual([99_000_000, 1_000_009]);
    expect(chunks.join("") === larger).toBe(true);
  });

  it("counts UTF-8 bytes and preserves CRLF, blank lines and password whitespace", () => {
    const content = "邮箱---- 密码 \r\n\r\nnext----🔑\nlast---- pass ";
    const { chunks } = createMicrosoftImportBatches(content, 26, 26);
    expect(chunks.length).toBeGreaterThan(1);
    expect(chunks.every((chunk) => new Blob([chunk]).size <= 26)).toBe(true);
    expect(chunks.slice(0, -1).every((chunk) => chunk.endsWith("\n"))).toBe(true);
    expect(chunks.join("")).toBe(content);
    expect(() => createMicrosoftImportBatches("密码".repeat(10), 26, 26)).toThrow("An import entry exceeds the upload limit.");
  });

  it("resumes polling an accepted batch without uploading it or completed batches again", async () => {
    const batches = createMicrosoftImportBatches("aa\nbb\ncc", 3, 3);
    const upload = vi.fn(async (_content: string, index: number) => ({ importId: index + 1, imported: 0 }));
    let failed = false;
    const poll = vi.fn(async (id: number) => {
      if (id === 2 && !failed) { failed = true; throw new Error("Network error"); }
      return { importId: id, status: "imported", imported: id + 3, skipped: id === 2 ? 1 : undefined, lastSafeError: id === 1 ? "Skipped 2 import entries." : "" };
    });
    const options = { upload, poll, onProgress: vi.fn(), signal: new AbortController().signal };
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("Network error");
    expect(batches).toMatchObject({ completed: 2, imported: 10 });
    expect(batches.parts[1].importId).toBe(2);
    await expect(runMicrosoftImportBatches(batches, options)).resolves.toBe(true);
    expect(upload.mock.calls.map(([chunk]) => chunk)).toEqual(["aa\n", "bb\n", "cc"]);
    expect(poll.mock.calls.map(([id]) => id)).toEqual([1, 2, 3, 2]);
    expect(batches).toMatchObject({ completed: 3, imported: 15, skipped: 3 });
  });

  it("stops after a failed batch and retains counts for committed data", async () => {
    const batches = createMicrosoftImportBatches("aa\nbb\ncc\ndd", 3, 3);
    const upload = vi.fn(async (_content: string, index: number) => ({ importId: index + 1, imported: 0 }));
    const poll = vi.fn()
      .mockResolvedValueOnce({ importId: 1, status: "failed", imported: 1, lastSafeError: "Invalid import format." })
      .mockResolvedValueOnce({ importId: 2, status: "imported", imported: 4 })
      .mockResolvedValueOnce({ importId: 3, status: "imported", imported: 5 });
    await expect(runMicrosoftImportBatches(batches, { upload, poll, onProgress: vi.fn(), signal: new AbortController().signal }))
      .rejects.toThrow("Invalid import format.");
    expect(upload).toHaveBeenCalledTimes(3);
    expect(batches).toMatchObject({ completed: 2, imported: 10, failed: true });
  });

  it("pauses on verification dismissal and does not advance while a batch is processing", async () => {
    const batches = createMicrosoftImportBatches("aa", 3, 3);
    const upload = vi.fn()
      .mockResolvedValueOnce(null)
      .mockResolvedValueOnce({ importId: 1, imported: 0 });
    const poll = vi.fn().mockResolvedValue({ importId: 1, status: "processing", imported: 0 });
    const options = { upload, poll, onProgress: vi.fn(), signal: new AbortController().signal };
    await expect(runMicrosoftImportBatches(batches, options)).resolves.toBe(false);
    expect(poll).not.toHaveBeenCalled();
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("still processing");
    expect(batches).toMatchObject({ completed: 0, failed: false });
    expect(batches.parts[0].importId).toBe(1);
  });

  it("stops continuations when polling finishes after cancellation", async () => {
    const batches = createMicrosoftImportBatches("aa\nbb", 3, 3);
    const controller = new AbortController();
    const upload = vi.fn(async () => ({ importId: 1, imported: 0 }));
    const poll = vi.fn(async () => {
      controller.abort();
      return { importId: 1, status: "imported", imported: 1 };
    });
    await expect(runMicrosoftImportBatches(batches, { upload, poll, onProgress: vi.fn(), signal: controller.signal }))
      .rejects.toMatchObject({ name: "AbortError" });
    expect(upload).toHaveBeenCalledTimes(2);
    expect(batches.completed).toBe(0);
  });

  it("runs no more than three uploads concurrently and fills freed slots", async () => {
    const batches = createMicrosoftImportBatches("a\nb\nc\nd\ne\nf", 2, 2);
    const releases: Array<() => void> = [];
    let active = 0;
    let peak = 0;
    const upload = vi.fn((_content: string, index: number) => {
      active += 1;
      peak = Math.max(peak, active);
      return new Promise<{ importId: number; status: string; imported: number }>((resolve) => {
        releases.push(() => { active -= 1; resolve({ importId: index + 1, status: "imported", imported: 1 }); });
      });
    });
    const run = runMicrosoftImportBatches(batches, { upload, poll: vi.fn(), onProgress: vi.fn(), signal: new AbortController().signal });
    expect(upload).toHaveBeenCalledTimes(3);
    for (let index = 0; index < 6; index += 1) {
      releases[index]();
      await vi.waitFor(() => expect(releases.length).toBe(Math.min(6, index + 4)));
    }
    await expect(run).resolves.toBe(true);
    expect(peak).toBe(3);
    expect(batches.completed).toBe(6);
  });

  it.each([new Error("Response lost"), new IamApiError(500, { message: "Server error" }), new IamApiError(408, { message: "Request timeout" })])("does not re-upload a non-idempotent request with an uncertain result", async (error) => {
    const batches = createMicrosoftImportBatches("one", 3, 3);
    const upload = vi.fn().mockRejectedValue(error);
    const options = { upload, poll: vi.fn(), onProgress: vi.fn(), signal: new AbortController().signal };
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("Upload result is unknown");
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("Upload result is unknown");
    expect(upload).toHaveBeenCalledOnce();
  });

  it("allows retry after a definite rejection without treating it as accepted", async () => {
    const batches = createMicrosoftImportBatches("one");
    const upload = vi.fn().mockRejectedValueOnce(new IamApiError(429, { message: "Too many requests." }))
      .mockResolvedValueOnce({ importId: 1, imported: 1, status: "imported" });
    const options = { upload, poll: vi.fn(), onProgress: vi.fn(), signal: new AbortController().signal };
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("Too many requests.");
    await expect(runMicrosoftImportBatches(batches, options)).resolves.toBe(true);
    expect(batches.uploadUncertain).toBe(false);
  });

  it("reuses each administrator batch's key after a lost upload response", async () => {
    const batches = createMicrosoftImportBatches("aa\nbb\ncc", 3, 3);
    let lostResponse = true;
    const upload = vi.fn(async (_content: string, index: number, _key?: string) => {
      if (index === 0 && lostResponse) { lostResponse = false; throw new Error("Response lost"); }
      return { importId: index + 1, imported: 1, status: "imported" };
    });
    const options = { upload, idempotentUploads: true, poll: vi.fn(), onProgress: vi.fn(), signal: new AbortController().signal };
    await expect(runMicrosoftImportBatches(batches, options)).rejects.toThrow("Response lost");
    await expect(runMicrosoftImportBatches(batches, options)).resolves.toBe(true);
    expect(upload).toHaveBeenCalledTimes(4);
    expect(upload.mock.calls[0][2]).toBeTruthy();
    expect(upload.mock.calls[0][2]).toBe(upload.mock.calls[3][2]);
    expect(new Set(upload.mock.calls.slice(0, 3).map((call) => call[2])).size).toBe(3);
    expect(batches.imported).toBe(3);
  });
});
