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

// Mirror the backend/persistence limits for immediate feedback. The backend
// remains authoritative because administrator imports upload the raw content.
const MICROSOFT_IMPORT_EMAIL_MAX_LENGTH = 255;
const MICROSOFT_IMPORT_PASSWORD_MAX_LENGTH = 512;
const MICROSOFT_IMPORT_CLIENT_ID_MAX_LENGTH = 255;
const MICROSOFT_IMPORT_REFRESH_TOKEN_MAX_LENGTH = 1024;
const MICROSOFT_IMPORT_BINDING_ADDRESS_MAX_LENGTH = 320;
const MICROSOFT_IMPORT_EMAIL_PATTERN = /^[^\s@]+@[^\s@]+$/;

export function preprocessMicrosoftImportContent(
  content: string,
  strategy: ImportErrorStrategy
): MicrosoftImportPreprocessResult {
  if (content.trim().length === 0) {
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

  const rawLines = content.split("\n");
  for (let index = 0; index < rawLines.length; index += 1) {
    const lineNumber = index + 1;
    const rawLine = (rawLines[index] ?? "").replace(/\r$/, "");
    if (rawLine.trim().length === 0) continue;

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
  const parts = line.split("----");
  if (![2, 3, 4, 5].includes(parts.length)) return null;

  const email = parts[0]?.trim() ?? "";
  const password = parts[1] ?? "";
  if (
    !isValidMicrosoftImportEmail(email, MICROSOFT_IMPORT_EMAIL_MAX_LENGTH) ||
    password.length === 0 ||
    exceedsMicrosoftImportLength(password, MICROSOFT_IMPORT_PASSWORD_MAX_LENGTH)
  ) {
    return null;
  }

  const entry: MicrosoftImportEntry = {
    email,
    password,
    clientID: "",
    refreshToken: "",
    bindingAddress: "",
  };

  if (parts.length === 3) {
    entry.bindingAddress = parts[2]?.trim() ?? "";
    if (
      !isValidMicrosoftImportEmail(
        entry.bindingAddress,
        MICROSOFT_IMPORT_BINDING_ADDRESS_MAX_LENGTH
      )
    ) {
      return null;
    }
  }
  if (parts.length === 4) {
    entry.clientID = parts[2]?.trim() ?? "";
    entry.refreshToken = parts[3]?.trim() ?? "";
    if (
      entry.clientID.length === 0 ||
      entry.refreshToken.length === 0 ||
      exceedsMicrosoftImportLength(
        entry.clientID,
        MICROSOFT_IMPORT_CLIENT_ID_MAX_LENGTH
      ) ||
      exceedsMicrosoftImportLength(
        entry.refreshToken,
        MICROSOFT_IMPORT_REFRESH_TOKEN_MAX_LENGTH
      )
    ) {
      return null;
    }
  }
  if (parts.length === 5) {
    entry.clientID = parts[2]?.trim() ?? "";
    entry.refreshToken = parts[3]?.trim() ?? "";
    entry.bindingAddress = parts[4]?.trim() ?? "";
    if (
      entry.clientID.length === 0 ||
      entry.refreshToken.length === 0 ||
      exceedsMicrosoftImportLength(
        entry.clientID,
        MICROSOFT_IMPORT_CLIENT_ID_MAX_LENGTH
      ) ||
      exceedsMicrosoftImportLength(
        entry.refreshToken,
        MICROSOFT_IMPORT_REFRESH_TOKEN_MAX_LENGTH
      ) ||
      !isValidMicrosoftImportEmail(
        entry.bindingAddress,
        MICROSOFT_IMPORT_BINDING_ADDRESS_MAX_LENGTH
      )
    ) {
      return null;
    }
  }

  return entry;
}

function isValidMicrosoftImportEmail(value: string, maxLength: number) {
  return (
    !exceedsMicrosoftImportLength(value, maxLength) &&
    MICROSOFT_IMPORT_EMAIL_PATTERN.test(value)
  );
}

function exceedsMicrosoftImportLength(value: string, maxLength: number) {
  return Array.from(value).length > maxLength;
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
