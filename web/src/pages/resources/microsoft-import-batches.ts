import { IamApiError } from "@/lib/api-client";
import { generateIdempotencyKey } from "@/lib/idempotency";

// Leave room for multipart fields below Cloudflare's 100 MB request limit.
export const MICROSOFT_IMPORT_BATCH_BYTES = 99_000_000;

export interface MicrosoftImportBatchProgress {
  current: number;
  total: number;
  polling: boolean;
}

interface ImportStatus {
  importId: number;
  status?: string;
  imported: number;
  skipped?: number;
  lastSafeError?: string | null;
}

// ponytail: retain batch text for retries in this dialog; use Blob slices if memory becomes limiting.
export function createMicrosoftImportBatches(
  content: string,
  maxBytes = MICROSOFT_IMPORT_BATCH_BYTES,
  splitAboveBytes = 100_000_000
) {
  const chunks: string[] = [];
  if (content.length > 0 && new Blob([content]).size <= splitAboveBytes) {
    chunks.push(content);
  } else {
    const encoder = new TextEncoder();
    let start = 0;
    let bytes = 0;
    for (let offset = 0; offset < content.length;) {
      const newline = content.indexOf("\n", offset);
      const end = newline < 0 ? content.length : newline + 1;
      const lineBytes = encoder.encode(content.slice(offset, end)).byteLength;
      if (lineBytes > maxBytes) throw new Error("An import entry exceeds the upload limit.");
      if (bytes + lineBytes > maxBytes) {
        chunks.push(content.slice(start, offset));
        start = offset;
        bytes = 0;
      }
      bytes += lineBytes;
      offset = end;
    }
    if (start < content.length) chunks.push(content.slice(start));
  }
  return {
    chunks,
    parts: chunks.map(() => ({
      started: false,
      completed: false,
      importId: undefined as number | undefined,
      idempotencyKey: undefined as string | undefined,
    })),
    completed: 0,
    imported: 0,
    skipped: 0,
    failed: false,
    uploadUncertain: false,
    paused: false,
  };
}

export type MicrosoftImportBatches = ReturnType<typeof createMicrosoftImportBatches>;

export async function runMicrosoftImportBatches(
  batches: MicrosoftImportBatches,
  options: {
    upload: (content: string, index: number, idempotencyKey?: string) => Promise<ImportStatus | null>;
    idempotentUploads?: boolean;
    poll: (importId: number) => Promise<ImportStatus>;
    onProgress: (progress: MicrosoftImportBatchProgress) => void;
    onResult?: (result: ImportStatus) => void;
    signal: AbortSignal;
  }
) {
  const { signal, onProgress } = options;
  if (batches.uploadUncertain) throw new Error("Upload result is unknown. Check imported emails before importing the remaining data.");
  if (batches.failed) throw new Error("This batch failed. Correct the remaining data before importing again.");
  batches.paused = false;
  let nextIndex = 0;
  const errors: unknown[] = [];
  const reportProgress = () => onProgress({
    current: batches.parts.filter((part) => part.started).length,
    total: batches.parts.length,
    polling: batches.parts.every((part) => part.completed || part.importId !== undefined),
  });

  const worker = async () => {
    while (!batches.paused && nextIndex < batches.parts.length) {
      const index = nextIndex++;
      const part = batches.parts[index];
      if (part.completed) continue;
      try {
        signal.throwIfAborted();
        part.started = true;
        reportProgress();
        let status: ImportStatus | undefined;
        if (part.importId === undefined) {
          if (options.idempotentUploads) part.idempotencyKey ??= generateIdempotencyKey();
          try {
            status = (await options.upload(batches.chunks[index], index, part.idempotencyKey)) ?? undefined;
          } catch (error) {
            signal.throwIfAborted();
            // A missing response or a server error can occur after acceptance.
            // Only the administrator endpoint supports idempotent uploads.
            const rejected = error instanceof IamApiError && [400, 401, 403, 404, 409, 413, 415, 422, 429].includes(error.status);
            if (!options.idempotentUploads && !rejected) {
              batches.uploadUncertain = true;
              throw new Error("Upload result is unknown. Check imported emails before importing the remaining data.");
            }
            throw error;
          }
          signal.throwIfAborted();
          if (!status) {
            batches.paused = true;
            return;
          }
          part.importId = status.importId;
          reportProgress();
        }
        if (!status?.status || status.status === "processing") {
          status = await options.poll(part.importId!);
          signal.throwIfAborted();
        }
        if (status.status === "processing") {
          throw new Error("The Microsoft resource import is still processing.");
        }
        batches.imported += status.imported;
        const skipped = /^Skipped (\d+) import entr(?:y|ies)\.$/.exec(status.lastSafeError ?? "");
        batches.skipped += status.skipped ?? Number(skipped?.[1] ?? 0);
        if (status.status === "failed") batches.failed = true;
        else {
          part.completed = true;
          batches.completed += 1;
        }
        options.onResult?.(status);
        reportProgress();
        if (status.status === "failed") throw new Error(status.lastSafeError || "Resource import failed.");
      } catch (error) {
        batches.paused = true;
        errors.push(error);
        return;
      }
    }
  };
  // Drain active workers before allowing a retry to touch the same batch state.
  await Promise.all(Array.from({ length: Math.min(3, batches.parts.length) }, worker));
  if (errors.length) throw errors[0];
  return !batches.paused;
}
