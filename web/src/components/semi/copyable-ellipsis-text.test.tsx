// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  copyText: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("@/lib/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("@douyinfe/semi-ui", () => ({
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
  Typography: {
    Text: ({ children, ellipsis, ...props }: { children?: ReactNode; ellipsis?: { rows?: number } }) => (
      <span data-ellipsis-rows={ellipsis?.rows} {...props}>
        {children}
      </span>
    ),
  },
}));

import {
  CopyableEllipsisText,
  mailExtractionLabelKey,
} from "./copyable-ellipsis-text";

describe("CopyableEllipsisText", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.copyText.mockResolvedValue(undefined);
  });

  afterEach(cleanup);

  it("uses the URL label only for absolute HTTP URLs", () => {
    expect(mailExtractionLabelKey("https://example.com/verify?id=1")).toBe("Email URL");
    expect(mailExtractionLabelKey(" HTTP://example.com/verify ")).toBe("Email URL");
    expect(mailExtractionLabelKey("mailto:user@example.com")).toBe("Verification code");
    expect(mailExtractionLabelKey("123456")).toBe("Verification code");
    expect(mailExtractionLabelKey("123456", "Quick verification code")).toBe(
      "Quick verification code"
    );
  });

  it("ellipsizes visually but copies the complete value on text click", async () => {
    const value = "https://example.com/activation/a-very-long-token";
    const parentClick = vi.fn();
    render(
      <div onClick={parentClick}>
        <CopyableEllipsisText text={value} />
      </div>
    );

    const text = screen.getByRole("button", { name: `Copy: ${value}` });
    expect(text).toHaveAttribute("data-ellipsis-rows", "1");
    expect(text).toHaveTextContent(value);

    fireEvent.click(text);

    await waitFor(() => expect(mocks.copyText).toHaveBeenCalledWith(value));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Copied");
    expect(parentClick).not.toHaveBeenCalled();
  });
});
