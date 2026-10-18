import type { DaumImportErrorStrategy } from "@/lib/daum-api";

export interface DaumImportPreprocessFailure {
  line: number;
  category: "invalid_format" | "duplicate_email" | "non_daum_domain";
  firstLine?: number;
}
export interface DaumImportPreprocessResult {
  content: string;
  validCount: number;
  skippedCount: number;
  firstFailure?: DaumImportPreprocessFailure;
}

export function preprocessOwnedDaumImport(content: string, strategy: DaumImportErrorStrategy): DaumImportPreprocessResult {
  const seen = new Map<string, number>();
  const accepted: string[] = [];
  let skippedCount = 0;
  const lines = content.replace(/^\uFEFF/, "").split("\n");
  for (let index = 0; index < lines.length; index += 1) {
    const raw = lines[index].replace(/\r$/, "");
    if (!raw.trim()) continue;
    const parts = raw.split("----");
    const email = (parts[0] ?? "").trim().toLowerCase();
    let failure: DaumImportPreprocessFailure | undefined;
    if (![2, 3, 5].includes(parts.length) || !validEmail(email)) {
      failure = { line: index + 1, category: "invalid_format" };
    } else if (!email.endsWith("@daum.net")) {
      failure = { line: index + 1, category: "non_daum_domain" };
    } else if (Array.from(email.split("@")[0]).length > 242 || !validPassword(parts[1]) || (parts.length > 2 && !validPassword(parts[2]))) {
      failure = { line: index + 1, category: "invalid_format" };
    } else if (parts.length === 5 && (!validPickupURL(parts[3].trim()) || !validEmail(parts[4].trim()))) {
      failure = { line: index + 1, category: "invalid_format" };
    } else if (seen.has(email)) {
      failure = { line: index + 1, category: "duplicate_email", firstLine: seen.get(email) };
    }
    if (failure) {
      if (strategy === "abort") return { content: "", validCount: 0, skippedCount, firstFailure: failure };
      skippedCount++;
      continue;
    }
    seen.set(email, index + 1);
    parts[0] = email;
    if (parts.length === 5) {
      parts[3] = parts[3].trim();
      parts[4] = parts[4].trim().toLowerCase();
    }
    accepted.push(parts.join("----"));
  }
  return { content: accepted.join("\n"), validCount: accepted.length, skippedCount };
}

function validEmail(value: string) {
  return Array.from(value).length <= 255 && /^[^\s@]+@[^\s@]+$/.test(value) && !/[\r\n\x00]/.test(value);
}
function validPassword(value: string) {
  return value.length > 0 && Array.from(value).length <= 512 && !/[\r\n\x00]/.test(value);
}
function validPickupURL(value: string) {
  if (value.length > 4096) return false;
  try {
    const url = new URL(value);
    const emails = url.searchParams.getAll("email");
    const tokens = url.searchParams.getAll("token");
    return (url.protocol === "http:" || url.protocol === "https:") && Boolean(url.hostname) &&
      !url.username && !url.password && !url.hash && ["/pickup", "/v1/pickup"].includes(url.pathname) &&
      emails.length === 1 && validEmail(emails[0].trim()) && tokens.length === 1 &&
      tokens[0].length > 0 && tokens[0].length <= 2048 && !/[\r\n\x00]/.test(tokens[0]);
  } catch {
    return false;
  }
}
