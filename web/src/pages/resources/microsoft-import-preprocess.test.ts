import { describe, expect, it } from "vitest";

import { preprocessMicrosoftImportContent } from "./microsoft-import-preprocess";

describe("preprocessMicrosoftImportContent", () => {
  it("preserves password whitespace while trimming other fields", () => {
    const result = preprocessMicrosoftImportContent(
      "  first@example.com----  password with spaces  \r\nsecond@example.com---- trailing ---- client ---- refresh ---- aux@example.com  ",
      "abort"
    );

    expect(result.firstFailure).toBeUndefined();
    expect(result.validCount).toBe(2);
    expect(result.content).toBe(
      "first@example.com----  password with spaces  \nsecond@example.com---- trailing ----client----refresh----aux@example.com"
    );
  });
});
