import type { ImportErrorStrategy } from "@/lib/proto-api";
import { PROTO_DEFAULT_EMAIL_SUFFIX } from "./proto-model";

export interface ProtoImportPreprocessFailure {
  line: number;
  category: "invalid_format" | "duplicate_email";
  firstLine?: number;
}
export interface ProtoImportPreprocessResult {
  content: string;
  validCount: number;
  skippedCount: number;
  firstFailure?: ProtoImportPreprocessFailure;
}

const MAX_PKL_BYTES = 8 * 1024 * 1024;

function validCredentialText(value: string, maxLength: number) {
  if (!value.length || value.length > maxLength * 2) return false;
  let length = 0;
  for (const char of value) {
    const code = char.codePointAt(0)!;
    if (++length > maxLength || code === 0 || code === 10 || code === 13 || (code >= 0xd800 && code <= 0xdfff)) return false;
  }
  return true;
}

function validPKLBase64(raw: string) {
  const value = raw.trim();
  if (!value.length || value.length % 4 !== 0) return false;
  const padding = value.endsWith("==") ? 2 : value.endsWith("=") ? 1 : 0;
  if ((value.length / 4) * 3 - padding > MAX_PKL_BYTES) return false;
  let last = 0;
  // Scan without a repeated-group regex or Array.from: a PKL can be 11 MiB
  // encoded, and neither a large regex stack nor a per-character array is needed.
  for (let index = 0; index < value.length - padding; index += 1) {
    const code = value.charCodeAt(index);
    if (code >= 65 && code <= 90) last = code - 65;
    else if (code >= 97 && code <= 122) last = code - 71;
    else if (code >= 48 && code <= 57) last = code + 4;
    else if (code === 43) last = 62;
    else if (code === 47) last = 63;
    else return false;
  }
  // Canonical Base64 requires zero unused bits in the last padded sextet.
  return padding === 2 ? (last & 15) === 0 : padding === 1 ? (last & 3) === 0 : true;
}

export function preprocessProtoImportContent(content: string, strategy: ImportErrorStrategy): ProtoImportPreprocessResult {
  const result: ProtoImportPreprocessResult = { content: "", validCount: 0, skippedCount: 0 };
  const seen = new Map<string, number>();
  const valid: string[] = [];
  for (const [index, raw] of content.replace(/^\uFEFF/, "").split("\n").entries()) {
    const line = raw.replace(/\r$/, "");
    if (!line.trim()) continue;
    const parts = line.split("----", 4);
    const validPartCount = parts.length === 2 || parts.length === 3;
    const account = (parts[0] ?? "").trim().toLowerCase();
    const email = validPartCount && account && !account.includes("@") ? account + PROTO_DEFAULT_EMAIL_SUFFIX : account;
    const password = parts[1] ?? "";
    let failure: ProtoImportPreprocessFailure | undefined;
    if (!validPartCount
      || !validCredentialText(email, 255) || !/^[^\s@]+@[^\s@]+$/.test(email)
      || !validCredentialText(password, 512) || /[\r\n\0]/.test(parts[0] ?? "")
      || (parts.length === 3 && !validPKLBase64(parts[2]))) {
      failure = { line: index + 1, category: "invalid_format" };
    } else if (seen.has(email)) {
      failure = { line: index + 1, category: "duplicate_email", firstLine: seen.get(email) };
    }
    if (failure) {
      if (strategy === "abort") return { ...result, firstFailure: failure };
      result.skippedCount += 1;
      continue;
    }
    seen.set(email, index + 1);
    valid.push(email + "----" + password + (parts.length === 3 ? "----" + parts[2] : ""));
  }
  result.content = valid.join("\n");
  result.validCount = valid.length;
  if (!valid.length) result.firstFailure = { line: 0, category: "invalid_format" };
  return result;
}
