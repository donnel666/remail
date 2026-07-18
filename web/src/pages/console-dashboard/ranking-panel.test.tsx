// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RankingPanel } from "./ranking-panel";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Card: ({ children, title }: { children: ReactNode; title: ReactNode }) => (
    <section>
      <header>{title}</header>
      {children}
    </section>
  ),
}));

afterEach(cleanup);

describe("RankingPanel", () => {
  it("keeps ranks 1-5 left, 6-10 right, and only puts an off-board user in row 6", () => {
    const items = Array.from({ length: 10 }, (_, index) => ({
      count: 100 - index,
      isCurrentUser: false,
      name: `User ${index + 1}`,
      rank: index + 1,
    }));
    const view = render(
      <RankingPanel
        currentUserRank={{ count: 0, isCurrentUser: true, name: "Me", rank: 2 }}
        items={items.slice(0, 1)}
        kind="today"
        loading={false}
        title="Today"
      />
    );

    expect(screen.getByTestId("ranking-left-column")).toHaveTextContent("User 1");
    expect(screen.getByTestId("ranking-left-column")).toHaveTextContent("Me");
    expect(screen.getByTestId("ranking-right-column")).not.toHaveTextContent("Me");
    expect(screen.queryByTestId("ranking-current-user-row")).not.toBeInTheDocument();

    view.rerender(
      <RankingPanel
        currentUserRank={{ count: 1, isCurrentUser: true, name: "Me", rank: 11 }}
        items={items}
        kind="today"
        loading={false}
        title="Today"
      />
    );

    const left = screen.getByTestId("ranking-left-column");
    const right = screen.getByTestId("ranking-right-column");
    for (let rank = 1; rank <= 5; rank += 1) {
      expect(within(left).getByText(`User ${rank}`, { exact: true })).toBeInTheDocument();
      expect(within(right).queryByText(`User ${rank}`, { exact: true })).not.toBeInTheDocument();
    }
    for (let rank = 6; rank <= 10; rank += 1) {
      expect(within(right).getByText(`User ${rank}`, { exact: true })).toBeInTheDocument();
      expect(within(left).queryByText(`User ${rank}`, { exact: true })).not.toBeInTheDocument();
    }
    expect(screen.getByTestId("ranking-current-user-row")).toHaveTextContent("#11");
    expect(screen.getByTestId("ranking-current-user-row")).toHaveTextContent("Me");
  });
});
