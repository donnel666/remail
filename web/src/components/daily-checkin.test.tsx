// @vitest-environment jsdom

import { StrictMode } from "react";
import { act, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  claimDailyCheckin: vi.fn(),
  auth: { currentUser: { id: 7 } as { id: number } | null },
  location: { pathname: "/dashboard" },
}));
vi.mock("@/lib/wallet-api", () => ({ claimDailyCheckin: mocks.claimDailyCheckin }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: mocks.auth.currentUser }) }));
vi.mock("@tanstack/react-router", () => ({ useLocation: () => ({ pathname: mocks.location.pathname }) }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string, values?: { amount?: string }) => values?.amount ? key.replace("{{amount}}", values.amount) : key }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Modal: ({ children, title, visible }: any) => visible ? <div aria-label={title} role="dialog">{children}</div> : null,
}));

import DailyCheckin from "./daily-checkin";

describe("DailyCheckin", () => {
  afterEach(() => {
    mocks.claimDailyCheckin.mockReset();
    mocks.auth.currentUser = { id: 7 };
    mocks.location.pathname = "/dashboard";
  });

  it("keeps one in-flight claim and shows its result after a route change in StrictMode", async () => {
    let resolveClaim!: (value: { enabled: boolean; firstClaim: boolean; rewardAmount: string }) => void;
    mocks.claimDailyCheckin.mockReturnValueOnce(new Promise((resolve) => { resolveClaim = resolve; }));
    const view = render(<StrictMode><DailyCheckin /></StrictMode>);
    await waitFor(() => expect(mocks.claimDailyCheckin).toHaveBeenCalledTimes(1));

    mocks.location.pathname = "/console";
    view.rerender(<StrictMode><DailyCheckin /></StrictMode>);
    expect(mocks.claimDailyCheckin).toHaveBeenCalledTimes(1);

    await act(async () => {
      resolveClaim({ enabled: true, firstClaim: true, rewardAmount: "50.00" });
    });
    expect((await screen.findByRole("dialog", { name: "每日签到" })).textContent).toContain("签到成功，获得奖励 50 Points");
  });
});
