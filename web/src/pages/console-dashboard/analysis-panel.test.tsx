// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import type { ReactNode } from "react";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { DashboardData } from "@/lib/dashboard-api";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => {
  const Tabs = Object.assign(
    ({ children }: { children?: ReactNode }) => <div>{children}</div>,
    { TabPane: ({ tab }: { tab: ReactNode }) => <div>{tab}</div> },
  );
  return {
    Card: ({ children, title }: { children?: ReactNode; title?: ReactNode }) => (
      <section>
        {title}
        {children}
      </section>
    ),
    Tabs,
  };
});

vi.mock("@visactor/react-vchart", () => ({
  VChart: ({ spec }: { spec: { type?: string } }) => (
    <div data-chart-type={spec.type} data-testid="chart">
      <span data-testid="chart-spec">{JSON.stringify(spec)}</span>
    </div>
  ),
  VChartCore: {
    ThemeManager: {
      registerTheme: vi.fn(),
      setCurrentTheme: vi.fn(),
      themeExist: () => true,
    },
  },
}));

vi.mock("@visactor/vchart-semi-theme", () => ({
  semiDesignDark: { name: "dark" },
  semiDesignLight: { name: "light" },
}));

import { DashboardAnalysisPanel } from "./analysis-panel";

function renderedChartSpec() {
  return JSON.parse(screen.getByTestId("chart-spec").textContent ?? "{}");
}

const data: DashboardData = {
  codeRatio: 67,
  historicalCodeRanking: [],
  historicalCurrentUserRank: { count: 0, name: "Me", rank: 1 },
  projectCodeRanking: [
    { count: 3, name: "Microsoft", rank: 1 },
    { count: 2, name: "Telegram", rank: 2 },
  ],
  projectSeries: [
    { name: "Microsoft", spend: [1, 2] },
    { name: "Telegram", spend: [2, 1] },
  ],
  purchaseRatio: 33,
  stats: {
    averageCodeReceiptSeconds: 30,
    averagePurchaseActivationSeconds: 45,
    codeSuccessRate: 75,
    historicalSpend: 20,
    purchaseActivationSuccessRate: 50,
    walletBalance: 100,
  },
  todayCodeRanking: [],
  todayCurrentUserRank: { count: 0, name: "Me", rank: 1 },
  trend: [
    {
      activatedPurchases: 1,
      averageCodeReceiptSeconds: 25,
      averagePurchaseActivationSeconds: 40,
      codeOrders: 2,
      label: "08:00",
      orders: 3,
      purchaseOrders: 1,
      receivedCodes: 1,
      spend: 3,
    },
    {
      activatedPurchases: 0,
      averageCodeReceiptSeconds: 35,
      averagePurchaseActivationSeconds: 0,
      codeOrders: 2,
      label: "09:00",
      orders: 3,
      purchaseOrders: 1,
      receivedCodes: 2,
      spend: 3,
    },
  ],
};

afterEach(cleanup);

describe("console dashboard analysis charts", () => {
  it("maps spend distribution and project ranking data into line charts", () => {
    const { rerender } = render(
      <DashboardAnalysisPanel
        data={data}
        loading={false}
        onViewChange={vi.fn()}
        view="spend"
      />,
    );

    expect(screen.getByTestId("chart")).toHaveAttribute("data-chart-type", "line");
    expect(renderedChartSpec()).toMatchObject({
      data: [
        {
          values: [
            { Project: "Microsoft", Time: "08:00", Usage: 1 },
            { Project: "Microsoft", Time: "09:00", Usage: 2 },
            { Project: "Telegram", Time: "08:00", Usage: 2 },
            { Project: "Telegram", Time: "09:00", Usage: 1 },
          ],
        },
      ],
      seriesField: "Project",
      xField: "Time",
      yField: "Usage",
    });

    rerender(
      <DashboardAnalysisPanel
        data={data}
        loading={false}
        onViewChange={vi.fn()}
        view="projects"
      />,
    );

    expect(screen.getByTestId("chart")).toHaveAttribute("data-chart-type", "line");
    expect(renderedChartSpec()).toMatchObject({
      data: [
        {
          values: [
            { Count: 3, Project: "Microsoft" },
            { Count: 2, Project: "Telegram" },
          ],
        },
      ],
      point: { visible: true },
      title: { text: "Project fulfillment ranking" },
      xField: "Project",
      yField: "Count",
    });
  });

  it("shows an empty state when the selected user has no project data", () => {
    const emptyProjectData = {
      ...data,
      projectCodeRanking: [],
      projectSeries: [],
    };
    const { rerender } = render(
      <DashboardAnalysisPanel
        data={emptyProjectData}
        loading={false}
        onViewChange={vi.fn()}
        view="spend"
      />,
    );

    expect(screen.queryByTestId("chart")).not.toBeInTheDocument();
    expect(screen.getByText("No overview data")).toBeVisible();

    rerender(
      <DashboardAnalysisPanel
        data={emptyProjectData}
        loading={false}
        onViewChange={vi.fn()}
        view="projects"
      />,
    );

    expect(screen.queryByTestId("chart")).not.toBeInTheDocument();
    expect(screen.getByText("No overview data")).toBeVisible();
  });
});
