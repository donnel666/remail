// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const mocks = vi.hoisted(() => ({
  list: vi.fn(), owners: vi.fn(), refresh: vi.fn(), setTotal: vi.fn(), translate: (key: string) => key,
  httpGet: vi.fn(), httpPost: vi.fn(), toastError: vi.fn(), toastSuccess: vi.fn(), selection: null as any,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: null }) }));
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
    Modal: { confirm: vi.fn(({ onOk }: any) => onOk()) }, Toast: { error: mocks.toastError, success: mocks.toastSuccess, info: vi.fn(), warning: vi.fn() },
  };
});
vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({ IllustrationNoResult: () => null, IllustrationNoResultDark: () => null }));
vi.mock("@/components/semi/card-pro", () => ({ CardPro: ({ actionsArea, tabsArea, children }: any) => <>{tabsArea}{actionsArea}{children}</> }));
vi.mock("@/components/semi/card-table", () => ({
  CardTable: ({ rowSelection }: any) => <div>
    <button onClick={() => rowSelection.onChange([41])}>Select resource 41</button>
    {mocks.selection?.selectedCount > 0 ? <>
      <button onClick={() => mocks.selection.extraActions.find((item: any) => item.key === "publish").onClick()}>Publish selected</button>
      <button onClick={() => mocks.selection.extraActions.find((item: any) => item.key === "private").onClick()}>Unpublish selected</button>
      <button onClick={() => mocks.selection.onDelete()}>Delete selected</button>
    </> : null}
  </div>, DESKTOP_TABLE_SCROLL_Y: 300,
}));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/compact-mode-toggle", () => ({ CompactModeToggle: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: () => null }));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: () => null, ownersWithCurrentUserFirst: (owners: unknown) => owners }));
vi.mock("@/hooks/use-debounced-value", () => ({ useDebouncedValue: (value: unknown) => [value, vi.fn()] }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("@/hooks/use-block-paged-list", async () => {
  const React = await import("react");
  return { useBlockPagedList: ({ filterKey, loadBlock }: any) => {
    React.useEffect(() => { void loadBlock(0, 20, undefined, new AbortController().signal); }, [filterKey]);
    return { loaded: false, loading: false, pagedItems: [], refresh: mocks.refresh, setTotal: mocks.setTotal, total: 0 };
  } };
});
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: (options: unknown) => { mocks.selection = options; } }));
vi.mock("@/lib/api-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api-client")>()),
  apiClient: { GET: mocks.httpGet, POST: mocks.httpPost },
}));
vi.mock("./admin-daum/daum-modals", () => ({ EditDaumModal: () => null, ImportDaumModal: () => null, ReplaceCredentialsModal: () => null }));
vi.mock("./admin-daum/daum-detail-sheet", () => ({ DaumDetailSheet: () => null }));
vi.mock("./admin-daum/daum-maintenance-modal", () => ({ DaumMaintenanceModal: () => null }));
vi.mock("./admin-daum/daum-bulk-maintenance-modal", () => ({ DaumBulkMaintenanceModal: () => null }));
vi.mock("./admin-daum/daum-meta", () => ({
  OwnerIdentity: () => null, renderStatusTag: () => null, formatTime: () => "",
  STATUS_META: Object.fromEntries(["pending", "validating", "identifying", "normal", "abnormal", "disabled", "deleted"].map((status) => [status, { label: status }])),
}));
vi.mock("@/lib/admin-daum-api", async (importOriginal) => ({ ...(await importOriginal<typeof import("@/lib/admin-daum-api")>()), listAdminDaumResources: mocks.list, listAdminDaumOwners: mocks.owners }));
import AdminDaumEmails from "./AdminDaumEmails";
beforeEach(() => {
  vi.clearAllMocks();
  mocks.selection = null;
  mocks.httpGet.mockReset();
  mocks.httpPost.mockReset();
  mocks.refresh.mockResolvedValue(undefined);
  mocks.list.mockResolvedValue({ items: [], total: 0 });
  mocks.owners.mockResolvedValue([]);
});
afterEach(cleanup);

it("keeps Microsoft-style suffix tabs while carrying the protocol filter through requests and reset", async () => {
  render(<AdminDaumEmails />);
  await waitFor(() => expect(mocks.list).toHaveBeenCalled());
  const tabs = screen.getByRole("tablist");
  expect(within(tabs).getAllByRole("tab")).toHaveLength(4);
  fireEvent.click(within(tabs).getByRole("tab", { name: /hanmail2.net/ }));
  fireEvent.click(screen.getByRole("button", { name: "POP3" }));
  await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith(
    { suffix: "hanmail2.net", protocol: "pop3" }, 0, 20, undefined, expect.objectContaining({ signal: expect.any(AbortSignal) }),
  ));
  fireEvent.click(screen.getByRole("button", { name: "Reset" }));
  await waitFor(() => expect(mocks.list).toHaveBeenLastCalledWith({}, 0, 20, undefined, expect.any(Object)));
});


it.each([
  ["Publish selected", "publish", "forbidden"],
  ["Unpublish selected", "unpublish", "network"],
  ["Delete selected", "delete", "forbidden"],
])("keeps the accepted task for %s and recovers progress without submitting again", async (label, action, failure) => {
  const accepted = { taskId: "daum_bulk:7", status: "queued", requested: 1, processed: 0, affected: 0, skipped: 0, reasonCounts: [] };
  mocks.httpPost.mockResolvedValue({ data: accepted, response: new Response(null, { status: 202 }) });
  let progressReads = 0;
  mocks.httpGet.mockImplementation(async (path: string) => {
    expect(path).toBe("/v1/admin/daum/resources/bulk-tasks/{taskId}");
    if (++progressReads <= 2) {
      if (failure === "network") throw new TypeError("Failed to fetch");
      return { error: { message: "Permission denied." }, response: new Response(null, { status: 403 }) };
    }
    return { data: { ...accepted, status: "succeeded", processed: 1, affected: 1 }, response: new Response(null, { status: 200 }) };
  });
  render(<AdminDaumEmails />);
  fireEvent.click(screen.getByRole("button", { name: "Select resource 41" }));
  fireEvent.click(screen.getByRole("button", { name: label }));
  await screen.findByText("daum_bulk:7");
  await screen.findByRole("alert");
  expect(mocks.toastError).not.toHaveBeenCalled();
  expect(mocks.toastSuccess).toHaveBeenCalledWith("Daum resources bulk operation submitted.");
  expect(mocks.httpPost).toHaveBeenCalledOnce();
  expect(mocks.httpPost.mock.calls[0][1].params.path.command).toBe(action);
  fireEvent.click(within(screen.getByRole("status")).getByRole("button", { name: "Refresh" }));
  await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Succeeded"));
  expect(mocks.httpPost).toHaveBeenCalledOnce();
  expect(mocks.refresh).toHaveBeenCalledTimes(2);
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
