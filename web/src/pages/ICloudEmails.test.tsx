// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import type { ICloudResource } from "@/lib/icloud-api";

const mocks = vi.hoisted(() => ({ list: vi.fn(), action: vi.fn(), actionByFilter: vi.fn(), selection: vi.fn(), tabs: vi.fn(), toast: vi.fn(), confirm: vi.fn() }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: { role: "supplier" }, refreshCurrentUser: vi.fn() }) }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/lib/icloud-api", () => ({ listICloudResources: mocks.list, applyICloudCommand: mocks.action, applyICloudCommandByFilter: mocks.actionByFilter, listICloudAliases: vi.fn() }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, disabled, onClick }: { children: ReactNode; disabled?: boolean; onClick?: () => void }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Input: ({ value, onChange, ...props }: { value: string; onChange: (value: string) => void; "aria-label"?: string }) => <input aria-label={props["aria-label"]} value={value} onChange={(event) => onChange(event.target.value)} />,
  DatePicker: () => null,
  Dropdown: ({ children, render: content }: { children: ReactNode; render: ReactNode }) => <div>{children}{content}</div>,
  Tabs: Object.assign(({ children, onChange }: { children: ReactNode; onChange: (value: string) => void }) => { mocks.tabs(onChange); return <div role="tablist">{children}</div>; }, {
    TabPane: ({ tab, itemKey }: { tab: ReactNode; itemKey: string }) => <button role="tab" onClick={() => mocks.tabs.mock.lastCall?.[0](itemKey)}>{tab}</button>,
  }),
  Tooltip: ({ children }: { children: ReactNode }) => <>{children}</>,
  Space: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  Tag: ({ children }: { children: ReactNode }) => <span>{children}</span>,
  Empty: () => null,
  Modal: Object.assign(() => null, { confirm: mocks.confirm }),
  Toast: { info: mocks.toast, error: mocks.toast, warning: mocks.toast, success: mocks.toast },
}));
vi.mock("@/components/semi/card-pro", () => ({ CardPro: (props: Record<string, ReactNode>) => <div>{props.tabsArea}{props.descriptionArea}{props.actionsArea}{props.searchArea}{props.children}{props.paginationArea}</div> }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: ({ onPageChange }: { onPageChange: (page: number) => void }) => <button onClick={() => onPageChange(11)}>Page 11</button> }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: ({ text }: { text: string }) => <span>{text}</span> }));
vi.mock("@/components/semi/statistic-filter-option", () => ({ StatisticFilterOption: ({ label, value, onSelect }: { label: string; value: string; onSelect: (value: string) => void }) => <button onClick={() => onSelect(value)}>{label}</button> }));
vi.mock("@/components/semi/card-table", () => ({ DESKTOP_TABLE_SCROLL_Y: "100%", CardTable: ({ dataSource, columns, rowSelection }: {
  dataSource: ICloudResource[];
  columns: { render?: (value: unknown, resource: ICloudResource) => ReactNode }[];
  rowSelection: { onChange: (keys: number[]) => void };
}) => <div>{dataSource.map((row) => <div data-testid={`row-${row.id}`} key={row.id}>
  <input type="checkbox" aria-label={`Select ${row.id}`} onChange={() => rowSelection.onChange([row.id])} />
  {columns.map((column, i) => <span key={i}>{column.render?.(null, row)}</span>)}
</div>)}</div> }));
vi.mock("./resources/import-icloud-emails-modal", () => ({ ImportICloudEmailsModal: ({ open }: { open: boolean }) => open ? <div>Apple import dialog</div> : null }));
vi.mock("./resources/supplier-application-modal", () => ({ ensureSupplierRole: async () => true, SupplierApplicationModal: () => null }));
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: mocks.selection }));
import ICloudEmails from "./ICloudEmails";

const resource = (id: number) => ({ id, version: 5, email: `apple${id}@example.com`, status: "normal", forSale: false, aliasCount: 2, selectedForwardTo: "relay@example.com", expireAt: "2026-11-01T00:00:00Z", createdAt: "2026-10-01T00:00:00Z", lastSafeError: null });
describe("Apple email console", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mocks.list.mockImplementation(async ({ offset }: { offset: number }) => ({ items: Array.from({ length: 100 }, (_, i) => resource(offset + i + 1)), total: 200 }));
    mocks.action.mockResolvedValue({ affected: 1, skipped: 0 });
    mocks.actionByFilter.mockResolvedValue({ affected: 200, skipped: 0 });
  });
  afterEach(cleanup);
  it("rejects a filter action above the batch limit before submitting", async () => {
    mocks.list.mockResolvedValue({ items: [resource(1)], total: 1001 });
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
    expect(mocks.toast).toHaveBeenCalledWith("iCloud bulk selection limit exceeded.");
    expect(mocks.actionByFilter).not.toHaveBeenCalled();
  });
  it("keeps Apple columns visible when switching the compact layout", async () => {
    mocks.list.mockResolvedValue({ items: [resource(1)], total: 1 });
    render(<ICloudEmails />);
    const row = await screen.findByTestId("row-1");
    expect(within(row).getByText("relay@example.com")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Compact list" }));
    expect(within(row).getByText("relay@example.com")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Adaptive list" })).toBeTruthy();
  });
  it("opens imports and sends the current selected resource version for validation", async () => {
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select 1" }));
    act(() => mocks.selection.mock.lastCall?.[0].onCheck());
    await waitFor(() => expect(mocks.action).toHaveBeenCalledWith("validate", [expect.objectContaining({ id: 1, version: 5 })]));
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    expect(screen.getByText("Apple import dialog")).toBeTruthy();
  });
  it("uses the suffix tabs and applies toolbar actions to the whole filter", async () => {
    mocks.list.mockResolvedValue({ items: [resource(1)], total: 150, facets: {
      status: { all: 150 }, forSale: { all: 150, no: 150, yes: 0 },
      suffixes: [{ key: "example.com", count: 150 }],
    } });
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getByRole("tab", { name: /example.com/ }));
    await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ suffix: "example.com", offset: 0 }), expect.any(AbortSignal)));
    fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
    await waitFor(() => expect(mocks.actionByFilter).toHaveBeenCalledWith("validate", expect.objectContaining({ suffix: "example.com" })));
    expect(mocks.action).not.toHaveBeenCalled();
  });
  it("keeps the confirmed filter when a bulk confirmation remains open", async () => {
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
    expect(mocks.confirm).toHaveBeenCalledTimes(1);
    const confirm = mocks.confirm.mock.lastCall?.[0].onOk;
    fireEvent.change(screen.getByRole("textbox", { name: "Search email or suffix" }), { target: { value: "changed" } });
    await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ search: "changed" }), expect.any(AbortSignal)), { timeout: 2500 });
    await act(async () => confirm());
    expect(mocks.actionByFilter).toHaveBeenCalledWith("delete", expect.objectContaining({ search: undefined }));
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ search: "changed" }), expect.any(AbortSignal));
  });
  it("loads the next 100-row block and applies search filters from the first page", async () => {
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Page 11" }));
    await screen.findByText("apple101@example.com");
    expect(mocks.list).toHaveBeenCalledWith(expect.objectContaining({ offset: 100, limit: 100 }), expect.any(AbortSignal));
    fireEvent.change(screen.getByRole("textbox", { name: "Search email or suffix" }), { target: { value: "apple1" } });
    await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ search: "apple1", offset: 0 }), expect.any(AbortSignal)), { timeout: 2000 });
  });
  it("disables deletion and publication of already published accounts", async () => {
    mocks.list.mockResolvedValue({ items: [{ ...resource(1), forSale: true }], total: 1 });
    render(<ICloudEmails />);
    const row = await screen.findByTestId("row-1");
    expect(within(row).getByRole("button", { name: "Delete" }).hasAttribute("disabled")).toBe(true);
    expect(within(row).getByRole("button", { name: "Sell" }).hasAttribute("disabled")).toBe(true);
  });
  it.each(["success", "failure"])("keeps the current filter when an earlier action finishes with %s", async (outcome) => {
    mocks.list.mockImplementation(async ({ search }: { search?: string }) => ({
      items: [{ ...resource(search ? 2 : 1), email: search ? "filtered@example.com" : "unfiltered@example.com" }], total: 1,
    }));
    let finish!: () => void;
    mocks.action.mockReturnValue(new Promise((resolve, reject) => {
      finish = () => outcome === "success" ? resolve({ affected: 1, skipped: 0 }) : reject(new Error("Request failed"));
    }));
    render(<ICloudEmails />);
    await screen.findByText("unfiltered@example.com");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select 1" }));
    act(() => mocks.selection.mock.lastCall?.[0].onCheck());
    await waitFor(() => expect(mocks.action).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByRole("textbox", { name: "Search email or suffix" }), { target: { value: "filtered" } });
    await screen.findByText("filtered@example.com", {}, { timeout: 2500 });
    await act(async () => finish());
    expect((screen.getByRole("textbox", { name: "Search email or suffix" }) as HTMLInputElement).value).toBe("filtered");
    expect(screen.queryByText("unfiltered@example.com")).toBeNull();
    expect(screen.getByText("filtered@example.com")).toBeTruthy();
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ search: "filtered", offset: 0 }), expect.any(AbortSignal));
  });
  it("refreshes the current page after an action started on an earlier page", async () => {
    let finish!: (value: { affected: number; skipped: number }) => void;
    mocks.action.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    render(<ICloudEmails />);
    await screen.findByText("apple1@example.com");
    fireEvent.click(screen.getByRole("checkbox", { name: "Select 1" }));
    act(() => mocks.selection.mock.lastCall?.[0].onCheck());
    await waitFor(() => expect(mocks.action).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "Page 11" }));
    await screen.findByText("apple101@example.com");
    await act(async () => finish({ affected: 1, skipped: 0 }));
    expect(screen.getByText("apple101@example.com")).toBeTruthy();
    expect(screen.queryByText("apple1@example.com")).toBeNull();
    expect(mocks.list).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 100 }), expect.any(AbortSignal));
  });
});
