// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import type { AdminDashboardData } from "@/lib/admin-dashboard-api";
import { AdminDashboardSummaryCards } from "./admin-summary-cards";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@douyinfe/semi-ui", () => {
  const Children = ({ children }: { children?: ReactNode }) => <>{children}</>;
  return {
    Card: ({ title, children }: { title?: ReactNode; children?: ReactNode }) => <section>{title}{children}</section>,
    Avatar: Children,
    Skeleton: Object.assign(Children, { Paragraph: () => null }),
  };
});

afterEach(cleanup);

it.each([125.750001, 0])("shows current withdrawable points (%s) without a withdrawal trend", (withdrawableAmount) => {
  const data = {
    stats: { withdrawableAmount, withdrawAmount: 60 },
    trend: [{ label: "10-01", withdrawAmount: 60 }],
    projectCodeRanking: [],
    projectInventoryRanking: [],
  } as unknown as AdminDashboardData;

  render(<AdminDashboardSummaryCards data={data} loading={false} />);

  const row = screen.getByText("Withdrawable points").closest(".justify-between")!;
  expect(within(row as HTMLElement).getByText(String(withdrawableAmount))).toBeInTheDocument();
  expect(row.querySelector("polyline")).toBeNull();
  expect(screen.queryByText("Withdraw amount")).not.toBeInTheDocument();
});
