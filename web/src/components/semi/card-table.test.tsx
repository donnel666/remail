// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => true }));

vi.mock("@douyinfe/semi-icons", () => ({
  IconChevronDown: () => null,
  IconChevronUp: () => null,
}));

vi.mock("@douyinfe/semi-ui", () => {
  const Skeleton: any = ({ placeholder }: any) => placeholder ?? null;
  Skeleton.Title = () => null;
  return {
    Button: ({ children }: any) => <button type="button">{children}</button>,
    Card: ({ children }: any) => <section>{children}</section>,
    Checkbox: ({
      "aria-label": ariaLabel,
      checked,
      className,
      disabled,
      onChange,
    }: any) => (
      <input
        aria-label={ariaLabel}
        checked={Boolean(checked)}
        className={className}
        disabled={disabled}
        onChange={(event) =>
          onChange?.({ target: { checked: event.target.checked } })
        }
        type="checkbox"
      />
    ),
    Collapsible: ({ children }: any) => children,
    Empty: () => null,
    Pagination: () => null,
    Skeleton,
    Table: () => null,
  };
});

import { CardTable } from "./card-table";

describe("CardTable mobile selection", () => {
  afterEach(() => cleanup());

  it("selects mobile cards and respects disabled rows", () => {
    const records = [
      { id: 1, name: "One" },
      { id: 2, name: "Two" },
    ];
    const onChange = vi.fn();
    const view = render(
      <CardTable
        columns={[{ dataIndex: "name", title: "Name" }]}
        dataSource={records}
        rowKey="id"
        rowSelection={{
          getCheckboxProps: (record) => ({ disabled: record.id === 2 }),
          onChange,
          selectedRowKeys: [],
        }}
      />
    );

    const first = screen.getByRole("checkbox", { name: "Select row: 1" });
    expect(first).toHaveClass("min-h-11", "min-w-11");
    expect(
      screen.getByRole("checkbox", { name: "Select row: 2" })
    ).toBeDisabled();

    fireEvent.click(first);
    expect(onChange).toHaveBeenLastCalledWith([1], [records[0]]);

    view.rerender(
      <CardTable
        columns={[{ dataIndex: "name", title: "Name" }]}
        dataSource={records}
        rowKey="id"
        rowSelection={{ onChange, selectedRowKeys: [1] }}
      />
    );
    fireEvent.click(screen.getByRole("checkbox", { name: "Select row: 1" }));
    expect(onChange).toHaveBeenLastCalledWith([], []);
  });
});
