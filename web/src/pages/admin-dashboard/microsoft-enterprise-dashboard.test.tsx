// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AdminDashboardData } from "@/lib/admin-dashboard-api";
import { AdminDashboardSummaryCards } from "./admin-summary-cards";
import { AdminDashboardAnalysisPanel } from "./admin-analysis-panel";

const chart = vi.hoisted(() => ({ render: vi.fn() }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@douyinfe/semi-ui", () => {
  const Children = ({ children }: { children?: ReactNode }) => <>{children}</>;
  return {
    Card: ({ title, children }: { title?: ReactNode; children?: ReactNode }) => <section>{title}{children}</section>,
    Avatar: Children,
    Skeleton: Object.assign(Children, { Paragraph: () => null }),
    Tabs: Object.assign(Children, { TabPane: () => null }),
  };
});
vi.mock("@visactor/react-vchart", () => ({
  VChart: ({ spec }: { spec: unknown }) => { chart.render(spec); return null; },
  VChartCore: { ThemeManager: { themeExist: () => true, setCurrentTheme: vi.fn() } },
}));
vi.mock("@visactor/vchart-semi-theme", () => ({ semiDesignDark: { name: "dark" }, semiDesignLight: { name: "light" } }));

const data: AdminDashboardData = {
  stats: {
    successfulCodeReceipts: 4,
    microsoftTotalEmails: 11, microsoftAvailableEmails: 7, microsoftCodeReceipts: 1, microsoftPurchaseActivations: 0,
    microsoftEnterpriseTotalEmails: 5, microsoftEnterpriseAvailableEmails: 3,
    microsoftEnterpriseCodeReceipts: 3, microsoftEnterprisePurchaseActivations: 2,
    microsoftEnterpriseCodeSuccessRate: 75, microsoftEnterprisePurchaseActivationSuccessRate: 50,
    microsoftEnterpriseAverageCodeReceiptSeconds: 40, microsoftEnterpriseAveragePurchaseActivationSeconds: 30,
  } as AdminDashboardData["stats"],
  trend: [{
    label: "10-01", successfulCodeReceipts: 4,
    microsoftTotalEmails: 11, microsoftAvailableEmails: 7, microsoftReceivedCodes: 1, microsoftCodeSuccessRate: 50,
    microsoftEnterpriseTotalEmails: 5, microsoftEnterpriseAvailableEmails: 3,
    microsoftEnterpriseReceivedCodes: 3, microsoftEnterpriseCodeSuccessRate: 75,
  } as AdminDashboardData["trend"][number]],
  projectCodeRanking: [], projectInventoryRanking: [],
};

afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe("Microsoft enterprise dashboard", () => {
  it("shows enterprise inventory and fulfillment in a separate card", () => {
    render(<AdminDashboardSummaryCards data={data} loading={false} />);
    const enterprise = within(screen.getByText("Microsoft Enterprise Emails").closest("section")!);
    expect(enterprise.getByText("Total Microsoft enterprise emails")).toBeInTheDocument();
    expect(enterprise.getByText("5")).toBeInTheDocument();
    expect(enterprise.getByText("3")).toBeInTheDocument();
    expect(enterprise.getByText("3 - 2")).toBeInTheDocument();
    const personal = within(screen.getByText("Microsoft Emails").closest("section")!);
    expect(personal.getByText("11")).toBeInTheDocument();
    expect(personal.getByText("1 - 0")).toBeInTheDocument();
  });

  it.each(["codes", "successRate", "inventory"] as const)("includes enterprise and personal series in %s", (view) => {
    render(<AdminDashboardAnalysisPanel data={data} loading={false} onViewChange={vi.fn()} view={view} />);
    const values = chart.render.mock.lastCall?.[0].data[0].values;
    const expected = {
      codes: [{ Metric: "Microsoft enterprise code receipts", Count: 3 }, { Metric: "Microsoft code receipts", Count: 1 }],
      successRate: [{ Metric: "Microsoft enterprise code success rate", Rate: 75 }, { Metric: "Microsoft code success rate", Rate: 50 }],
      inventory: [{ Metric: "Total Microsoft enterprise emails", Count: 5 }, { Metric: "Available Microsoft enterprise emails", Count: 3 }, { Metric: "Total Microsoft emails", Count: 11 }],
    }[view];
    expect(values).toEqual(expect.arrayContaining(expected.map((value) => expect.objectContaining(value))));
  });
});
