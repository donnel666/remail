// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  list: vi.fn(), validate: vi.fn(), validateBatch: vi.fn(), validateFilter: vi.fn(), publish: vi.fn(), publishBatch: vi.fn(), publishFilter: vi.fn(),
  remove: vi.fn(), removeBatch: vi.fn(), removeFilter: vi.fn(), error: vi.fn(), success: vi.fn(), warning: vi.fn(), confirm: vi.fn(), supplier: vi.fn(),
  selection: null as any, role: "supplier", userId: 7, translate: (key: string) => key,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: { id: mocks.userId, role: mocks.role }, refreshCurrentUser: vi.fn() }) }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Container = ({ children }: any) => <div>{children}</div>;
  const Tabs = ({ children, onChange }: any) => <div role="tablist">{React.Children.map(children, (child: any) => <button role="tab" onClick={() => onChange(child.props.itemKey)}>{child.props.tab}</button>)}</div>;
  Tabs.TabPane = () => null;
  return {
    Button: ({ children, onClick, disabled, loading }: any) => <button disabled={disabled || loading} onClick={onClick}>{children}</button>,
    Tabs, Tag: Container, Tooltip: Container, Space: Container, Empty: () => null, DatePicker: () => null,
    Dropdown: ({ children, render: content }: any) => <div>{children}{content}</div>,
    Input: ({ value, onChange, placeholder }: any) => <input value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} />,
    Modal: { confirm: mocks.confirm }, Toast: { error: mocks.error, success: mocks.success, info: vi.fn(), warning: mocks.warning },
  };
});
vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({ IllustrationNoResult: () => null, IllustrationNoResultDark: () => null }));
vi.mock("@/components/semi/card-pro", () => ({ CardPro: ({ tabsArea, actionsArea, children }: any) => <>{tabsArea}{actionsArea}{children}</> }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: ({ text }: any) => <span>{text}</span> }));
vi.mock("@/components/semi/statistic-filter-option", () => ({ StatisticFilterOption: ({ label, value, onSelect }: any) => <button onClick={() => onSelect(value)}>{label}</button> }));
vi.mock("@/components/semi/card-table", () => ({ DESKTOP_TABLE_SCROLL_Y: 300, CardTable: ({ columns, dataSource, rowSelection }: any) => <>
  <button onClick={() => rowSelection.onChange(dataSource.map((item: any) => item.id))}>Select all loaded</button>
  <table><thead><tr>{columns.map((column: any) => <th key={column.key}>{column.title}</th>)}</tr></thead><tbody>
    {dataSource.map((item: any) => <tr key={item.id} data-testid={`row-${item.id}`}>{columns.map((column: any) => <td key={column.key}>{column.render(item[column.dataIndex], item)}</td>)}</tr>)}
  </tbody></table>
</> }));
vi.mock("./resources/import-owned-daum-emails-modal", () => ({ ImportOwnedDaumEmailsModal: ({ open }: any) => open ? <section role="dialog">Daum import</section> : null }));
vi.mock("./resources/supplier-application-modal", () => ({ ensureSupplierRole: mocks.supplier, SupplierApplicationModal: ({ open }: any) => open ? <section role="dialog">Supplier application</section> : null }));
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: (options: any) => { mocks.selection = options; } }));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("@/lib/daum-api", async (importOriginal) => ({
  ...await importOriginal<typeof import("@/lib/daum-api")>(),
  listOwnedDaumResources: mocks.list, validateDaumResource: mocks.validate, validateDaumResourcesBatch: mocks.validateBatch, validateDaumResourcesByFilter: mocks.validateFilter,
  publishDaumResource: mocks.publish, publishDaumResourcesBatch: mocks.publishBatch, publishDaumResourcesByFilter: mocks.publishFilter,
  deleteDaumResource: mocks.remove, deleteDaumResourcesBatch: mocks.removeBatch, deleteDaumResourcesByFilter: mocks.removeFilter,
}));
import { IamApiError } from "@/lib/api-client";
import DaumEmails from "./DaumEmails";

const resource = (id: number, forSale = false) => ({
  id, version: 7, type: "daum", email: `user${id}@daum.net`, suffix: "daum.net", status: "normal",
  forSale, longLived: true, qualityScore: 100, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
});
const response = {
  items: [resource(41), resource(42, true)], total: 2, nextAfterId: null,
  facets: {
    status: { all: 2, normal: 2, pending: 0, validating: 0, identifying: 0, abnormal: 0, disabled: 0 },
    forSale: { all: 2, yes: 1, no: 1 }, longLived: { all: 2, yes: 2, no: 0 }, suffixes: [{ key: "daum.net", count: 2 }],
  },
};
const bulkResult = { taskId: "daum_bulk:7", status: "succeeded", affected: 1 };
beforeEach(() => {
  vi.resetAllMocks();
  mocks.selection = null;
  mocks.role = "supplier";
  mocks.userId = 7;
  mocks.list.mockResolvedValue(response);
  mocks.validate.mockResolvedValue({ queued: true });
  mocks.validateBatch.mockResolvedValue({ requested: 2 });
  mocks.validateFilter.mockResolvedValue({ requested: 2 });
  mocks.publish.mockResolvedValue({ version: 8, forSale: true });
  mocks.remove.mockResolvedValue({});
  mocks.publishBatch.mockResolvedValue(bulkResult);
  mocks.publishFilter.mockResolvedValue(bulkResult);
  mocks.removeBatch.mockResolvedValue(bulkResult);
  mocks.removeFilter.mockResolvedValue(bulkResult);
  mocks.supplier.mockResolvedValue(true);
  mocks.confirm.mockImplementation(({ onOk }: any) => onOk());
});
afterEach(cleanup);

it("copies the console Microsoft list and only displays the supported suffix", async () => {
  render(<DaumEmails />);
  await screen.findByText("user41@daum.net");
  expect(screen.getAllByRole("columnheader").map((item) => item.textContent)).toEqual(["Suffix", "Email", "Status", "Private", "Long-lived", "Action"]);
  expect(within(screen.getByRole("tablist")).getAllByRole("tab")).toHaveLength(2);
  expect(document.body.textContent).not.toMatch(/hanmail|Graph|Explicit aliases|Task details|Details/);
  const published = screen.getByTestId("row-42");
  expect(within(published).getByRole("button", { name: "Sell" })).toBeDisabled();
  expect(within(published).getByRole("button", { name: "Delete" })).toBeDisabled();
  fireEvent.click(screen.getByRole("button", { name: "Import" }));
  expect(screen.getByRole("dialog")).toHaveTextContent("Daum import");
});

it("uses the current suffix and state filters for toolbar validation and reset", async () => {
  render(<DaumEmails />);
  await screen.findByText("user41@daum.net");
  fireEvent.click(screen.getByRole("tab", { name: /daum.net/ }));
  fireEvent.click(screen.getByRole("button", { name: "Normal" }));
  await screen.findByText("user41@daum.net");
  fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
  await waitFor(() => expect(mocks.validateFilter).toHaveBeenCalledWith({ status: "normal", suffix: "daum.net" }, expect.objectContaining({ run: expect.any(Function) })));
  fireEvent.click(screen.getByRole("button", { name: "Reset" }));
  await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith({}, 0, expect.any(Number), undefined, expect.any(Object)));
});

it("submits row commands through owned APIs and uses the resource version", async () => {
  render(<DaumEmails />);
  const row = await screen.findByTestId("row-41");
  fireEvent.click(within(row).getByRole("button", { name: "Check" }));
  await waitFor(() => expect(mocks.validate).toHaveBeenCalledWith(41, expect.any(AbortSignal)));
  fireEvent.click(within(row).getByRole("button", { name: "Sell" }));
  await waitFor(() => expect(mocks.publish).toHaveBeenCalledWith(41, 7));
});

it("preserves the shared supplier application flow before publishing", async () => {
  mocks.role = "user";
  mocks.supplier.mockImplementation(async (_role, _refresh, open) => { open(); return false; });
  render(<DaumEmails />);
  const row = await screen.findByTestId("row-41");
  fireEvent.click(within(row).getByRole("button", { name: "Sell" }));
  await screen.findByRole("dialog");
  expect(screen.getByRole("dialog")).toHaveTextContent("Supplier application");
  expect(mocks.supplier).toHaveBeenCalledWith("user", expect.any(Function), expect.any(Function));
  expect(mocks.publish).not.toHaveBeenCalled();
});

it("only publishes selected private resources and retains accepted bulk success after a polling interruption", async () => {
  mocks.publishBatch.mockResolvedValue({ taskId: "daum_bulk:7", status: "queued", affected: 0 });
  render(<DaumEmails />);
  await screen.findByText("user41@daum.net");
  fireEvent.click(screen.getByRole("button", { name: "Select all loaded" }));
  await act(async () => { mocks.selection.onSell(); });
  await waitFor(() => expect(mocks.publishBatch).toHaveBeenCalledWith([41], expect.objectContaining({ run: expect.any(Function) })));
  await waitFor(() => expect(mocks.success).toHaveBeenCalledWith("Daum resources bulk operation submitted."));
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(mocks.publishBatch).toHaveBeenCalledOnce();
  expect(mocks.error).not.toHaveBeenCalled();
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((accept, decline) => { resolve = accept; reject = decline; });
  return { promise, resolve, reject };
}

function useDifferentRowsForStatusFilters() {
  const abnormal = {
    ...response, items: [{ ...resource(99), status: "abnormal" }], total: 1,
    facets: { ...response.facets, status: { ...response.facets.status, all: 1, normal: 0, abnormal: 1 } },
  };
  mocks.list.mockImplementation(async (filter) => filter.status === "abnormal" ? abnormal : response);
}

it.each([
  ["Check", "validate"], ["Sell", "publish"], ["Delete", "remove"],
] as const)("refreshes the current Abnormal list after delayed row %s", async (label, command) => {
  const pending = deferred<object>();
  mocks[command].mockReturnValueOnce(pending.promise);
  useDifferentRowsForStatusFilters();
  render(<DaumEmails />);
  const original = await screen.findByTestId("row-41");
  fireEvent.click(within(original).getByRole("button", { name: label }));
  await waitFor(() => expect(mocks[command]).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button", { name: "Abnormal" }));
  await screen.findByTestId("row-99");
  const readsBeforeCompletion = mocks.list.mock.calls.length;

  await act(async () => { pending.resolve({ queued: true }); });
  await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(readsBeforeCompletion));
  expect(screen.getByTestId("row-99")).toBeInTheDocument();
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
  expect(mocks.list.mock.calls.slice(readsBeforeCompletion).map((call) => call[0])).toEqual([
    { status: "abnormal" }, { status: "abnormal" },
  ]);
});

it.each([
  ["Check", "validateFilter"], ["Sell", "publishFilter"], ["Delete", "removeFilter"],
  ["Check", "validateBatch"], ["Sell", "publishBatch"], ["Delete", "removeBatch"],
] as const)("refreshes the current list after delayed bulk %s (%s)", async (label, command) => {
  const pending = deferred<object>();
  mocks[command].mockReturnValueOnce(pending.promise);
  useDifferentRowsForStatusFilters();
  render(<DaumEmails />);
  await screen.findByTestId("row-41");
  if (command.endsWith("Batch")) {
    fireEvent.click(screen.getByRole("button", { name: "Select all loaded" }));
    act(() => { mocks.selection[`on${label}`](); });
  } else {
    fireEvent.click(screen.getAllByRole("button", { name: label })[0]);
  }
  await waitFor(() => expect(mocks[command]).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button", { name: "Abnormal" }));
  await screen.findByTestId("row-99");
  const readsBeforeCompletion = mocks.list.mock.calls.length;

  await act(async () => { pending.resolve(bulkResult); });
  await waitFor(() => expect(mocks.list.mock.calls.length).toBeGreaterThan(readsBeforeCompletion));
  expect(screen.getByTestId("row-99")).toBeInTheDocument();
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
  expect(mocks.list.mock.calls.slice(readsBeforeCompletion).map((call) => call[0])).toEqual([
    { status: "abnormal" }, { status: "abnormal" },
  ]);
});

it("uses the current filter when a previously opened confirmation is accepted later", async () => {
  mocks.confirm.mockImplementation(() => undefined);
  useDifferentRowsForStatusFilters();
  render(<DaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  const confirm = mocks.confirm.mock.calls[0][0].onOk;
  fireEvent.click(screen.getByRole("button", { name: "Abnormal" }));
  await screen.findByTestId("row-99");
  const readsBeforeCompletion = mocks.list.mock.calls.length;

  await act(async () => { await confirm(); });
  expect(mocks.removeFilter.mock.calls[0][0]).toEqual({});
  expect(mocks.list.mock.calls.slice(readsBeforeCompletion).map((call) => call[0])).toEqual([
    { status: "abnormal" }, { status: "abnormal" },
  ]);
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
});

it("keeps the retry session through refresh and warns about an unknown bulk result", async () => {
  mocks.validateFilter.mockRejectedValueOnce(new TypeError("Response lost"));
  render(<DaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
  await waitFor(() => expect(mocks.warning).toHaveBeenCalledWith("Daum bulk operation outcome is not confirmed. Retry to confirm."));
  expect(mocks.error).not.toHaveBeenCalled();
  const session = mocks.validateFilter.mock.calls[0][1];
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByTestId("row-41");
  expect(mocks.validateFilter).toHaveBeenCalledOnce();
  fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
  await waitFor(() => expect(mocks.validateFilter).toHaveBeenCalledTimes(2));
  expect(mocks.validateFilter.mock.calls[1][1]).toBe(session);
});

it("reports a known request rejection through the normal error message", async () => {
  mocks.removeFilter.mockRejectedValueOnce(new IamApiError(403, { message: "Forbidden" }));
  render(<DaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Delete failed."));
  expect(mocks.warning).not.toHaveBeenCalled();
});

it.each(["validate", "removeFilter"] as const)("ignores a late %s response after unmount", async (command) => {
  const pending = deferred<object>();
  mocks[command].mockReturnValueOnce(pending.promise);
  const { unmount } = render(<DaumEmails />);
  const row = await screen.findByTestId("row-41");
  if (command === "validate") fireEvent.click(within(row).getByRole("button", { name: "Check" }));
  else fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks[command]).toHaveBeenCalledOnce());
  const readsBeforeUnmount = mocks.list.mock.calls.length;
  unmount();
  await act(async () => { pending.resolve(bulkResult); });
  expect(mocks.list).toHaveBeenCalledTimes(readsBeforeUnmount);
  expect(mocks.success).not.toHaveBeenCalled();
  expect(mocks.error).not.toHaveBeenCalled();
  if (command === "validate") expect(mocks.validate.mock.calls[0][1].aborted).toBe(true);
});

it("isolates retry sessions and ignores old mutation completions when the authenticated user changes", async () => {
  const pending = deferred<object>();
  mocks.validateFilter.mockReturnValueOnce(pending.promise);
  const { rerender } = render(<DaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
  await waitFor(() => expect(mocks.validateFilter).toHaveBeenCalledOnce());
  const oldSession = mocks.validateFilter.mock.calls[0][1];
  mocks.userId = 8;
  rerender(<DaumEmails />);
  await screen.findByTestId("row-41");
  const readsBeforeCompletion = mocks.list.mock.calls.length;
  await act(async () => { pending.resolve(bulkResult); });
  expect(mocks.list).toHaveBeenCalledTimes(readsBeforeCompletion);
  expect(mocks.success).not.toHaveBeenCalled();

  fireEvent.click(screen.getAllByRole("button", { name: "Check" })[0]);
  await waitFor(() => expect(mocks.validateFilter).toHaveBeenCalledTimes(2));
  expect(mocks.validateFilter.mock.calls[1][1]).not.toBe(oldSession);
});
