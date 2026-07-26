// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  getUserGroups: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/iam-api", () => ({
  getUserGroups: mocks.getUserGroups,
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Box = ({ children }: { children?: React.ReactNode }) => <div>{children}</div>;
  const Button = ({ children, disabled, loading, onClick }: any) => (
    <button disabled={disabled || loading} onClick={onClick}>{children}</button>
  );
  return {
    Button,
    Card: Box,
    Tag: Box,
    Typography: { Text: Box },
  };
});

import { MembershipOverview } from "./membership";
import type { MembershipGroup } from "@/lib/membership";

const current: MembershipGroup = {
  id: 1,
  code: "normal",
  name: "Normal",
  description: "",
  enabled: true,
  apiConcurrencyLimit: 3,
  priceDiscountRatio: "1.00",
  topupThreshold: "0.00",
  autoUpgradeEnabled: false,
};

const next: MembershipGroup = {
  ...current,
  id: 2,
  code: "vip",
  name: "VIP",
  topupThreshold: "100.00",
  autoUpgradeEnabled: true,
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("MembershipOverview", () => {
  it("announces group loading failures and retries both membership sources", async () => {
    mocks.getUserGroups
      .mockRejectedValueOnce(new Error("failed"))
      .mockResolvedValueOnce({ groups: [current, next] });
    const onRetry = vi.fn().mockResolvedValue(undefined);

    render(
      <MembershipOverview
        currentGroup={current}
        loading={false}
        onRetry={onRetry}
        totalRecharged="20.00"
      />,
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Membership tier information is unavailable",
    );
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.getByRole("progressbar")).toBeInTheDocument());
    expect(mocks.getUserGroups).toHaveBeenCalledTimes(2);
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("does not present a missing recharge total as zero progress", async () => {
    mocks.getUserGroups.mockResolvedValue({ groups: [current, next] });

    render(
      <MembershipOverview
        currentGroup={current}
        loading={false}
        totalRecharged={undefined}
      />,
    );

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("-")).toBeInTheDocument();
    expect(screen.queryByText("￥0.00")).not.toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });
});
