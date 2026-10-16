// @vitest-environment jsdom
import { describe, expect, it, vi } from "vitest";
import type { AdminMicrosoftAsyncTaskKind } from "./admin-microsoft-types";
import { taskKindLabel } from "./microsoft-meta";

vi.mock("@douyinfe/semi-ui", () => ({ Avatar: () => null, Tag: () => null, Tooltip: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: () => null }));

describe("task kind labels", () => {
  it.each(["liveness", "liveness_smtp"] as const)("labels %s as a liveness check", (kind) => {
    expect(taskKindLabel(kind)).toBe("Liveness check");
  });

  it("keeps an unexpected backend kind visible", () => {
    expect(taskKindLabel("future_task" as AdminMicrosoftAsyncTaskKind)).toBe("future_task");
  });
});
