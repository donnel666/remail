// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
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
  apiConcurrencyLimit: 0,
  topupThreshold: "100.00",
  autoUpgradeEnabled: true,
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("MembershipOverview", () => {
  it("announces group loading failures and retries the group table", async () => {
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

    const [topAlert] = await screen.findAllByRole("alert");
    expect(topAlert).toHaveTextContent(
      "Membership tier information is unavailable",
    );
    fireEvent.click(within(topAlert).getByRole("button", { name: "Try again" }));

    await waitFor(() => expect(screen.getByRole("progressbar")).toBeInTheDocument());
    expect(mocks.getUserGroups).toHaveBeenCalledTimes(2);
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("keeps the compact summary, progress, and all groups together", async () => {
    mocks.getUserGroups.mockResolvedValue({ groups: [current, next] });

    render(
      <MembershipOverview
        currentGroup={current}
        loading={false}
        totalRecharged="20.00"
      />,
    );

    expect(await screen.findByRole("progressbar")).toHaveAttribute("aria-valuenow", "20");
    expect(screen.getAllByText("Multiplier")).toHaveLength(2);
    expect(screen.getByText("Maximum concurrency", { selector: "th" })).toBeInTheDocument();
    expect(screen.getByText("3", { selector: "strong" })).toBeInTheDocument();
    const table = screen.getByRole("table", { name: "Membership groups" });
    expect(within(table).getByText("Normal")).toBeInTheDocument();
    expect(within(table).getByText("VIP")).toBeInTheDocument();
    expect(within(table).getByText("Uses API key or system limit")).toBeInTheDocument();
  });

  it("does not present a missing recharge total as zero progress", async () => {
    mocks.getUserGroups.mockResolvedValue({ groups: [current, next] });
    render(<MembershipOverview currentGroup={current} totalRecharged={undefined} />);

    await screen.findByRole("table", { name: "Membership groups" });
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Membership tier information is unavailable",
    );
  });
});
