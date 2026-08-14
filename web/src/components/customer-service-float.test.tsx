// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  copyText: vi.fn(),
  getCustomerService: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Popover: ({ children, content, visible }: any) => <>{children}{visible ? content : null}</>,
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

vi.mock("@/lib/clipboard", () => ({ copyText: mocks.copyText }));
vi.mock("@/lib/system-settings-api", () => ({
  CUSTOMER_SERVICE_UPDATED_EVENT: "customer-service-updated",
  getCustomerService: mocks.getCustomerService,
}));

import { CustomerServiceFloat } from "./customer-service-float";

describe("CustomerServiceFloat", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => cleanup());

  it("stays hidden when no customer service link is configured", async () => {
    mocks.getCustomerService.mockResolvedValue({
      qqGroupNumber: "",
      qqGroupUrl: "",
      telegramGroupUrl: "",
    });

    render(<CustomerServiceFloat />);

    await waitFor(() => expect(mocks.getCustomerService).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: "联系客服" })).not.toBeInTheDocument();
  });

  it("reloads the links after customer service settings are saved", async () => {
    mocks.getCustomerService
      .mockResolvedValueOnce({ qqGroupNumber: "", qqGroupUrl: "", telegramGroupUrl: "" })
      .mockResolvedValueOnce({
        qqGroupNumber: "123456789",
        qqGroupUrl: "https://qm.qq.com/q/example",
        telegramGroupUrl: "",
      });

    render(<CustomerServiceFloat />);
    await waitFor(() => expect(mocks.getCustomerService).toHaveBeenCalledTimes(1));

    window.dispatchEvent(new Event("customer-service-updated"));

    expect(await screen.findByRole("button", { name: "联系客服" })).toBeInTheDocument();
    expect(mocks.getCustomerService).toHaveBeenCalledTimes(2);
  });

  it("copies the QQ number and opens the configured group links", async () => {
    mocks.getCustomerService.mockResolvedValue({
      qqGroupNumber: "123456789",
      qqGroupUrl: "https://qm.qq.com/q/example",
      telegramGroupUrl: "https://t.me/example",
    });
    mocks.copyText.mockResolvedValue(undefined);

    render(<CustomerServiceFloat />);
    fireEvent.click(await screen.findByRole("button", { name: "联系客服" }));

    expect(screen.getByRole("link", { name: "打开QQ群" })).toHaveAttribute("href", "https://qm.qq.com/q/example");
    expect(screen.getByRole("link", { name: "打开 Telegram 群" })).toHaveAttribute("href", "https://t.me/example");

    fireEvent.click(screen.getByRole("button", { name: "复制QQ群号" }));
    await waitFor(() => expect(mocks.copyText).toHaveBeenCalledWith("123456789"));
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Copied");
  });
});
