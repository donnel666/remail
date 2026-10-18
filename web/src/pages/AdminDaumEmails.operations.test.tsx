// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({
  list: vi.fn(), owners: vi.fn(), translate: (key: string) => key,
  httpGet: vi.fn(), httpPost: vi.fn(), toastError: vi.fn(), toastSuccess: vi.fn(), toastWarning: vi.fn(), selection: null as any,
  userId: 7, detailProps: null as any, credentialsProps: null as any, editProps: null as any, nextKey: 0,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: { id: mocks.userId } }) }));
vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Children = ({ children }: any) => <>{children}</>;
  const Tabs = ({ children, onChange }: any) => <div role="tablist">{React.Children.map(children, (child: any) => <button role="tab" onClick={() => onChange(child.props.itemKey)}>{child.props.tab}</button>)}</div>;
  Tabs.TabPane = () => null;
  return {
    Button: ({ children, onClick }: any) => <button onClick={onClick}>{children}</button>,
    Tabs, Tag: Children, Tooltip: Children, Space: Children, Empty: () => null, DatePicker: () => null,
    Dropdown: ({ children, render }: any) => <div>{children}{render}</div>,
    Input: ({ value, onChange, placeholder }: any) => <input value={value} placeholder={placeholder} onChange={(event) => onChange(event.target.value)} />,
    Modal: Object.assign(({children,onOk,onCancel,okText,title,visible}: any) => visible ? <section role="dialog" aria-label={title}>{children}<button onClick={onOk}>{okText}</button><button onClick={onCancel}>Cancel maintenance</button></section> : null, { confirm: vi.fn(({ onOk }: any) => onOk()) }), Toast: { error: mocks.toastError, success: mocks.toastSuccess, info: vi.fn(), warning: mocks.toastWarning },
  };
});
vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({ IllustrationNoResult: () => null, IllustrationNoResultDark: () => null }));
vi.mock("@/components/semi/card-pro", () => ({ CardPro: ({ actionsArea, tabsArea, children }: any) => <>{tabsArea}{actionsArea}{children}</> }));
vi.mock("@/components/semi/card-table", () => ({
  CardTable: ({ rowSelection, columns, dataSource }: any) => <div>
    <table><thead><tr>{columns.map((column: any) => <th key={column.key}>{column.title}</th>)}</tr></thead>
      <tbody>{dataSource.map((item: any) => <tr key={item.id} data-testid={`row-${item.id}`}>{columns.map((column:any) => <td key={column.key}>{column.render(item[column.dataIndex],item)}</td>)}</tr>)}</tbody>
    </table>
    <button onClick={() => rowSelection.onChange([41])}>Select resource 41</button>
    <button onClick={() => rowSelection.onChange([99])}>Select resource 99</button>
    {mocks.selection?.selectedCount > 0 ? <>
      <button onClick={() => mocks.selection.extraActions.find((item: any) => item.key === "publish").onClick()}>Publish selected</button>
      <button onClick={() => mocks.selection.extraActions.find((item: any) => item.key === "private").onClick()}>Unpublish selected</button>
      <button onClick={() => mocks.selection.onDelete()}>Delete selected</button>
      <button onClick={() => mocks.selection.onSell()}>Disable selected</button>
      <button onClick={() => mocks.selection.onCheck()}>Maintain selected</button>
    </> : null}
  </div>, DESKTOP_TABLE_SCROLL_Y: 300,
}));
vi.mock("@/components/semi/statistic-filter-option", () => ({ StatisticFilterOption: ({label,value,onSelect}: any) => <button onClick={() => onSelect(value)}>{label}</button> }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/compact-mode-toggle", () => ({ CompactModeToggle: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: () => null }));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: () => <select aria-label="Owner filter" />, ownersWithCurrentUserFirst: (owners: unknown) => owners }));
vi.mock("@/hooks/use-debounced-value", () => ({ useDebouncedValue: (value: unknown) => [value, vi.fn()] }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: (options: unknown) => { mocks.selection = options; } }));
vi.mock("@/lib/api-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-client")>()),
  apiClient: { GET: mocks.httpGet, POST: mocks.httpPost },
}));
vi.mock("./admin-daum/daum-modals", () => ({
  EditDaumModal: (props: any) => { mocks.editProps=props; return null; },
  ImportDaumModal: () => null,
  ReplaceCredentialsModal: (props: any) => { mocks.credentialsProps=props; return null; },
}));
vi.mock("./admin-daum/daum-detail-sheet", () => ({ DaumDetailSheet: (props: any) => {
  mocks.detailProps=props;
  return props.detail ? <section role="dialog" aria-label="Resource detail" data-testid={`detail-${props.detail.id}`}>
    <span>{props.detail.emailAddress}</span><button onClick={props.onCancel}>Close detail</button><button onClick={props.onReplaceCredentials}>Replace credentials</button>
  </section> : null;
} }));
vi.mock("./admin-daum/daum-maintenance-modal", () => ({ DaumMaintenanceModal: () => null }));
vi.mock("./admin-daum/daum-meta", () => ({
  OwnerIdentity: () => null, renderStatusTag: () => null, formatTime: () => "",
  STATUS_META: Object.fromEntries(["pending", "validating", "identifying", "normal", "abnormal", "disabled", "deleted"].map((status) => [status, { label: status }])),
}));
vi.mock("@/lib/admin-daum-api", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/admin-daum-api")>()), listAdminDaumResources: mocks.list, listAdminDaumOwners: mocks.owners }));
import { Modal } from "@douyinfe/semi-ui";
import AdminDaumEmails from "./AdminDaumEmails";
vi.mock("@/lib/idempotency", () => ({generateIdempotencyKey: () => `admin-key-${++mocks.nextKey}`}));
beforeEach(() => {
  vi.resetAllMocks();
  mocks.userId=7; mocks.nextKey=0;
  vi.mocked(Modal.confirm).mockImplementation(({onOk}: any) => onOk());
  mocks.selection = null;
  mocks.httpGet.mockReset();
  mocks.httpPost.mockReset();
  mocks.list.mockResolvedValue({ items: [], total: 0 });
  mocks.owners.mockResolvedValue([]);
});
afterEach(cleanup);

const adminResource = (id: number, status: string) => ({id, version:1, email:`user${id}@daum.net`,emailAddress:`user${id}@daum.net`, suffix:"daum.net",status,forSale:false,longLived:false,ownerUserId:7,qualityScore:100,createdAt:"2026-10-01T00:00:00Z",updatedAt:"2026-10-01T00:00:00Z"});
it("keeps the current Abnormal filter when selected deletion finishes late", async () => {
  let finish!: (value: any) => void;
  mocks.httpPost.mockReturnValueOnce(new Promise(resolve => {finish=resolve;}));
  const done = {taskId:"daum_bulk:1", status:"succeeded",requested:1,processed:1,affected:1,skipped:0,reasonCounts:[]};
  mocks.httpGet.mockResolvedValue({data:done,response:new Response(null,{status:200})});
  mocks.list.mockImplementation(async (filter) => ({items:[adminResource(filter.status === "abnormal" ? 99 : 41, filter.status || "normal")],total:1,nextAfterId:null}));
  render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getByRole("button",{name:"Select resource 41"}));
  fireEvent.click(screen.getByRole("button",{name:"Delete selected"}));
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button",{name:"abnormal"}));
  await screen.findByTestId("row-99");
  await act(async()=>{finish({data:done,response:new Response(null,{status:202})});});
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
  expect(screen.getByTestId("row-99")).toBeInTheDocument();
});
it("reports a terminal bulk failure as failure", async () => {
  const result={taskId:"daum_bulk:1",status:"failed",requested:10,processed:4,affected:3,skipped:1,reasonCounts:[{reason:"resource_busy",count:1}]};
  mocks.httpPost.mockResolvedValue({data:{...result,status:"queued"},response:new Response(null,{status:202})});
  mocks.httpGet.mockResolvedValue({data:result,response:new Response(null,{status:200})});
  mocks.list.mockResolvedValue({items:[adminResource(41,"normal")],total:1,nextAfterId:null});
  render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getByRole("button",{name:"Select resource 41"}));
  fireEvent.click(screen.getByRole("button",{name:"Delete selected"}));
  await waitFor(()=>expect(mocks.httpGet).toHaveBeenCalledOnce());
  await act(async()=>{});
  expect(mocks.toastSuccess).not.toHaveBeenCalled();
  expect(mocks.toastError).toHaveBeenCalledWith("Daum resource operation failed. Processed: 4/10 · Succeeded: 3 · Skipped: 1 · Reason: resource_busy: 1");
});

const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 1, processed: 0, affected: 0, skipped: 0, reasonCounts: [] };
const postedKeys = () => mocks.httpPost.mock.calls.map((call) => call[1].params.header["Idempotency-Key"]);
function deferred() {
  let resolve!: (value: unknown) => void;
  const promise = new Promise((finish) => { resolve = finish; });
  return { promise, resolve };
}
function differentFilterRows() {
  mocks.list.mockImplementation(async (filter) => ({ items: [adminResource(filter.status === "abnormal" ? 99 : 41, filter.status || "normal")], total: 1, nextAfterId: null }));
}

it("retries unknown filter deletion after refresh without deleting inventory added after the original task", async () => {
  let inventory = [41];
  const tasks = new Map<string, typeof accepted>();
  let loseResponse = true;
  mocks.list.mockImplementation(async () => ({ items: inventory.map((id) => adminResource(id, "normal")), total: inventory.length, nextAfterId: null }));
  mocks.httpPost.mockImplementation(async (_path, options) => {
    const key = options.params.header["Idempotency-Key"];
    if (!tasks.has(key)) {
      tasks.set(key, { ...accepted, taskId: `daum_bulk:${tasks.size + 1}`, status: "succeeded", affected: inventory.length, processed: inventory.length });
      inventory = [];
    }
    if (loseResponse) { loseResponse = false; throw new TypeError("Response lost after deletion finished"); }
    return { data: tasks.get(key), response: new Response(null, { status: 202 }) };
  });
  render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.toastWarning).toHaveBeenCalledWith("Daum bulk operation outcome is not confirmed. Retry to confirm."));
  inventory.push(99);
  fireEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByTestId("row-99");
  expect(mocks.httpPost).toHaveBeenCalledOnce();
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledTimes(2));
  expect(tasks.size).toBe(1);
  expect(inventory).toEqual([99]);
  expect(new Set(postedKeys()).size).toBe(1);
  expect(mocks.toastError).not.toHaveBeenCalled();
});

it.each(["ids", "filter"] as const)("retains unknown maintenance %s operations when the dialog is closed and reopened", async (mode) => {
  mocks.list.mockResolvedValue({ items: [adminResource(41, "normal")], total: 1, nextAfterId: null });
  mocks.httpPost.mockRejectedValueOnce(new TypeError("Response lost")).mockResolvedValue({ data: accepted, response: new Response(null, { status: 202 }) });
  render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  const open = () => {
    if (mode === "ids") {
      fireEvent.click(screen.getByRole("button", { name: "Select resource 41" }));
      fireEvent.click(screen.getByRole("button", { name: "Maintain selected" }));
    } else fireEvent.click(screen.getAllByRole("button", { name: "Maintenance" })[0]);
    fireEvent.click(screen.getByRole("button", { name: /Scan projects/ }));
  };
  open();
  fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));
  await waitFor(() => expect(mocks.toastWarning).toHaveBeenCalled());
  fireEvent.click(screen.getByRole("button", { name: "Cancel maintenance" }));
  open();
  fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledTimes(2));
  expect(new Set(postedKeys()).size).toBe(1);
  expect(mocks.httpPost.mock.calls.map((call) => call[1].params.path.command)).toEqual(["history", "history"]);
});

it.each(["Put on sale", "Convert to private", "Delete"])("refreshes the current filter after a delayed filter %s", async (label) => {
  differentFilterRows();
  const pending = deferred();
  mocks.httpPost.mockReturnValueOnce(pending.promise);
  render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: label })[0]);
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button", { name: "abnormal" }));
  await screen.findByTestId("row-99");
  const reads = mocks.list.mock.calls.length;
  await act(async () => { pending.resolve({ data: accepted, response: new Response(null, { status: 202 }) }); });
  const refreshedFilters = mocks.list.mock.calls.slice(reads).map((call) => call[0]);
  expect(refreshedFilters.length).toBeGreaterThan(0);
  for (const filter of refreshedFilters) expect(filter).toEqual({ status: "abnormal" });
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
  expect(screen.getByTestId("row-99")).toBeInTheDocument();
});

it("refreshes the current filter after a delayed row command", async () => {
  differentFilterRows();
  const pending = deferred();
  mocks.httpPost.mockReturnValueOnce(pending.promise);
  render(<AdminDaumEmails />);
  const row = await screen.findByTestId("row-41");
  fireEvent.click(within(row).getByRole("button", { name: "Disable" }));
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledOnce());
  fireEvent.click(screen.getByRole("button", { name: "abnormal" }));
  await screen.findByTestId("row-99");
  const reads = mocks.list.mock.calls.length;
  await act(async () => { pending.resolve({ data: { resourceId: 41, status: "disabled" }, response: new Response(null, { status: 200 }) }); });
  const refreshedFilters = mocks.list.mock.calls.slice(reads).map((call) => call[0]);
  expect(refreshedFilters.length).toBeGreaterThan(0);
  for (const filter of refreshedFilters) expect(filter).toEqual({ status: "abnormal" });
  expect(screen.queryByTestId("row-41")).not.toBeInTheDocument();
});

it.each(["close", "switch"])("does not reopen or replace the current detail after a delayed credential save (%s)", async (action) => {
  mocks.list.mockResolvedValue({ items: [adminResource(41, "normal"), adminResource(99, "normal")], total: 2, nextAfterId: null });
  mocks.httpGet.mockImplementation(async (_path, options) => ({ data: adminResource(options.params.path.resourceId, "normal"), response: new Response(null, { status: 200 }) }));
  render(<AdminDaumEmails />);
  const row = await screen.findByTestId("row-41");
  fireEvent.click(within(row).getByRole("button", { name: "Details" }));
  await screen.findByTestId("detail-41");
  fireEvent.click(screen.getByRole("button", { name: "Replace credentials" }));
  const finishSave = mocks.credentialsProps.onSaved;
  fireEvent.click(screen.getByRole("button", { name: "Close detail" }));
  if (action === "switch") {
    fireEvent.click(within(screen.getByTestId("row-99")).getByRole("button", { name: "Details" }));
    await screen.findByTestId("detail-99");
  }
  const reads = mocks.httpGet.mock.calls.length;
  await act(async () => { await finishSave({ ...adminResource(41, "normal"), version: 2 }); });
  expect(mocks.httpGet).toHaveBeenCalledTimes(reads);
  expect(screen.queryByTestId("detail-41")).not.toBeInTheDocument();
  if (action === "switch") expect(screen.getByTestId("detail-99")).toBeInTheDocument();
});

it("isolates unknown operations and late responses when the authenticated user changes", async () => {
  mocks.list.mockResolvedValue({ items: [adminResource(41, "normal")], total: 1, nextAfterId: null });
  const pending = deferred();
  mocks.httpPost.mockReturnValueOnce(pending.promise).mockResolvedValue({ data: accepted, response: new Response(null, { status: 202 }) });
  const { rerender } = render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledOnce());
  mocks.userId = 8;
  rerender(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  const reads = mocks.list.mock.calls.length;
  await act(async () => { pending.resolve({ data: accepted, response: new Response(null, { status: 202 }) }); });
  expect(mocks.list).toHaveBeenCalledTimes(reads);
  expect(mocks.toastSuccess).not.toHaveBeenCalled();
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledTimes(2));
  expect(new Set(postedKeys()).size).toBe(2);
});

it("ignores late mutation completion after unmount", async () => {
  mocks.list.mockResolvedValue({ items: [adminResource(41, "normal")], total: 1, nextAfterId: null });
  const pending = deferred();
  mocks.httpPost.mockReturnValueOnce(pending.promise);
  const { unmount } = render(<AdminDaumEmails />);
  await screen.findByTestId("row-41");
  fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]);
  await waitFor(() => expect(mocks.httpPost).toHaveBeenCalledOnce());
  const reads = mocks.list.mock.calls.length;
  unmount();
  await act(async () => { pending.resolve({ data: accepted, response: new Response(null, { status: 202 }) }); });
  expect(mocks.list).toHaveBeenCalledTimes(reads);
  expect(mocks.toastSuccess).not.toHaveBeenCalled();
  expect(mocks.toastError).not.toHaveBeenCalled();
});
