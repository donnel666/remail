// @vitest-environment jsdom

import type { TFunction } from "i18next";
import { describe, expect, it, vi } from "vitest";

vi.mock("@douyinfe/semi-ui", () => ({
  Button: () => null,
  Modal: () => null,
  Space: () => null,
  TextArea: () => null,
  Toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
  Typography: { Text: () => null },
}));

import { getImportWarningMessage } from "./import-microsoft-emails-modal";

const translations: Record<string, string> = {
  "Import skipped errors": "{{count}} 行存在错误，已跳过。",
  "Resource import completed with warnings.": "资源导入已完成，但存在警告。",
};

const t = ((key: string, options?: Record<string, unknown>) => {
  let value = translations[key] ?? String(options?.defaultValue ?? key);
  for (const [name, replacement] of Object.entries(options ?? {})) {
    value = value.replace(`{{${name}}}`, String(replacement));
  }
  return value;
}) as TFunction;

describe("Microsoft import warning messages", () => {
  it("localizes singular, plural, and unknown backend summaries", () => {
    expect(getImportWarningMessage(t, "Skipped 1 import entry.")).toBe(
      "1 行存在错误，已跳过。"
    );
    expect(getImportWarningMessage(t, "Skipped 2 import entries.")).toBe(
      "2 行存在错误，已跳过。"
    );
    expect(getImportWarningMessage(t, "untranslated backend warning")).toBe(
      "资源导入已完成，但存在警告。"
    );
  });
});
