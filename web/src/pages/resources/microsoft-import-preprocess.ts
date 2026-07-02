import type { ImportErrorStrategy } from "@/lib/resources-api";

export interface MicrosoftImportPreprocessFailure {
  line: number;
  category: "invalid_format" | "duplicate_email";
  email?: string;
  firstLine?: number;
}

export interface MicrosoftImportPreprocessResult {
  content: string;
  validCount: number;
  skippedCount: number;
  firstFailure?: MicrosoftImportPreprocessFailure;
}

interface MicrosoftImportEntry {
  email: string;
  password: string;
  clientID: string;
  refreshToken: string;
  bindingAddress: string;
}

export function preprocessMicrosoftImportContent(
  content: string,
  strategy: ImportErrorStrategy
): MicrosoftImportPreprocessResult {
  const trimmedContent = content.trim();
  if (trimmedContent.length === 0) {
    return {
      content: "",
      validCount: 0,
      skippedCount: 0,
      firstFailure: { line: 0, category: "invalid_format" },
    };
  }

  const seen = new Map<string, number>();
  const validLines: string[] = [];
  let skippedCount = 0;

  const rawLines = trimmedContent.split("\n");
  for (let index = 0; index < rawLines.length; index += 1) {
    const lineNumber = index + 1;
    const rawLine = rawLines[index]?.trim() ?? "";
    if (rawLine.length === 0) continue;

    const parsed = parseMicrosoftImportLine(rawLine);
    if (!parsed) {
      const failure: MicrosoftImportPreprocessFailure = {
        line: lineNumber,
        category: "invalid_format",
        email: getPotentialEmail(rawLine),
      };
      if (strategy === "abort") {
        return {
          content: "",
          validCount: 0,
          skippedCount,
          firstFailure: failure,
        };
      }
      skippedCount += 1;
      continue;
    }

    const key = parsed.email.toLowerCase();
    const firstLine = seen.get(key);
    if (firstLine !== undefined) {
      const failure: MicrosoftImportPreprocessFailure = {
        line: lineNumber,
        category: "duplicate_email",
        email: parsed.email,
        firstLine,
      };
      if (strategy === "abort") {
        return {
          content: "",
          validCount: 0,
          skippedCount,
          firstFailure: failure,
        };
      }
      skippedCount += 1;
      continue;
    }

    seen.set(key, lineNumber);
    validLines.push(serializeMicrosoftImportEntry(parsed));
  }

  return {
    content: validLines.join("\n"),
    validCount: validLines.length,
    skippedCount,
  };
}

function parseMicrosoftImportLine(line: string): MicrosoftImportEntry | null {
  const parts = line.split("----").map((part) => part.trim());
  if (![2, 3, 4, 5].includes(parts.length)) return null;

  const email = parts[0] ?? "";
  const password = parts[1] ?? "";
  if (email.length === 0 || password.length === 0) return null;

  const entry: MicrosoftImportEntry = {
    email,
    password,
    clientID: "",
    refreshToken: "",
    bindingAddress: "",
  };

  if (parts.length === 3) {
    entry.bindingAddress = parts[2] ?? "";
    if (entry.bindingAddress.length === 0) return null;
  }
  if (parts.length === 4) {
    entry.clientID = parts[2] ?? "";
    entry.refreshToken = parts[3] ?? "";
    if (entry.clientID.length === 0 || entry.refreshToken.length === 0) {
      return null;
    }
  }
  if (parts.length === 5) {
    entry.clientID = parts[2] ?? "";
    entry.refreshToken = parts[3] ?? "";
    entry.bindingAddress = parts[4] ?? "";
    if (
      entry.clientID.length === 0 ||
      entry.refreshToken.length === 0 ||
      entry.bindingAddress.length === 0
    ) {
      return null;
    }
  }

  return entry;
}

function serializeMicrosoftImportEntry(entry: MicrosoftImportEntry) {
  if (entry.clientID && entry.refreshToken && entry.bindingAddress) {
    return [
      entry.email,
      entry.password,
      entry.clientID,
      entry.refreshToken,
      entry.bindingAddress,
    ].join("----");
  }
  if (entry.clientID && entry.refreshToken) {
    return [entry.email, entry.password, entry.clientID, entry.refreshToken].join(
      "----"
    );
  }
  if (entry.bindingAddress) {
    return [entry.email, entry.password, entry.bindingAddress].join("----");
  }
  return [entry.email, entry.password].join("----");
}

function getPotentialEmail(line: string) {
  return line.split("----")[0]?.trim();
}
