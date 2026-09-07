import type { ImportErrorStrategy } from "@/lib/proto-api";

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
export function preprocessProtoImportContent(content: string, strategy: ImportErrorStrategy): ProtoImportPreprocessResult {
  const result: ProtoImportPreprocessResult = { content: "", validCount: 0, skippedCount: 0 };
  const seen = new Map<string, number>();
  const valid: string[] = [];
  for (const [index, raw] of content.replace(/^\uFEFF/, "").split("\n").entries()) {
    const line = raw.replace(/\r$/, "");
    if (!line.trim()) continue;
    const parts = line.split("----");
    const email = (parts[0] ?? "").trim().toLowerCase();
    const password = parts[1] ?? "";
    let failure: ProtoImportPreprocessFailure | undefined;
    if (parts.length !== 2 || !/^[^\s@]+@[^\s@]+$/.test(email) || Array.from(email).length > 255 || password.length === 0 || Array.from(password).length > 512 || /[\r\n\0]/.test(line) || Array.from(line).some((char) => { const code = char.codePointAt(0)!; return code >= 0xd800 && code <= 0xdfff; })) {
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
    valid.push(email + "----" + password);
  }
  result.content = valid.join("\n");
  result.validCount = valid.length;
  if (!valid.length) result.firstFailure = { line: 0, category: "invalid_format" };
  return result;
}
