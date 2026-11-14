// @vitest-environment jsdom
import { act, cleanup, fireEvent, render, renderHook, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  userId: 1 as number | null, confirm: vi.fn(), checkMicrosoft: vi.fn(), checkDomain: vi.fn(), checkDaum: vi.fn(),
  checkProto: vi.fn(), checkProxies: vi.fn(), sync: vi.fn(), list: vi.fn(),
  refresh: vi.fn(), setTotal: vi.fn(), updateLoadedItems: vi.fn(),
  inventory: vi.fn(),
  items: [
    { id: 1, accountId: 11, phoneId: 1, status: "normal" },
    { id: 2, accountId: 11, phoneId: 2, status: "normal" },
    { id: 3, accountId: 22, phoneId: 3, status: "normal" },
  ],
  total: 3,
  translate: vi.fn((key: string) => key),
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/context/auth-provider", async (original) => ({
  ...await original<typeof import("@/context/auth-provider")>(),
  useAuth: () => ({ currentUser: mocks.userId === null ? null : { id: mocks.userId, role: "super_admin", permissions: ["core:resource:read", "core:resource:write", "core:resource:operate", "system:settings:write"] }, refreshCurrentUser: vi.fn() }),
}));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("@/hooks/use-block-paged-list", () => ({ useBlockPagedList: () => ({
  loadedItems: mocks.items, pagedItems: mocks.items, total: mocks.total, loading: false,
  refresh: mocks.refresh, setTotal: mocks.setTotal, updateLoadedItems: mocks.updateLoadedItems,
}) }));
vi.mock("@douyinfe/semi-ui", () => {
  const Container = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
  const Hidden = () => null;
  return {
    Button: ({ children, onClick, disabled }: { children?: ReactNode; onClick?: () => void; disabled?: boolean }) => <button onClick={onClick} disabled={disabled}>{children}</button>,
    Modal: Object.assign(Hidden, { confirm: mocks.confirm }),
    Dropdown: ({ children, render: content }: { children?: ReactNode; render?: ReactNode }) => <div>{children}{content}</div>,
    Tabs: Object.assign(Container, { TabPane: Hidden }),
    Form: Object.assign(Container, { Slot: Container }),
    DatePicker: Hidden, Input: Hidden, TextArea: Hidden, Table: Hidden,
    Select: ({ "aria-label": label, onChange, optionList }: { "aria-label"?: string; onChange: (value: string) => void; optionList?: { label: string; value: number }[] }) => <select aria-label={label} onChange={(event) => onChange(event.target.value)}><option value="" />{optionList?.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select>,
    Empty: Hidden, SideSheet: Hidden, Banner: Hidden, InputNumber: Hidden, Switch: Hidden,
    Tag: Container, Tooltip: Container, Space: Container, Typography: { Text: Container },
    Toast: { info: vi.fn(), success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  };
});
vi.mock("@/components/semi/card-pro", () => ({ CardPro: ({ actionsArea }: { actionsArea: ReactNode }) => <>{actionsArea}</> }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/statistic-filter-option", () => ({ StatisticFilterOption: ({ label, value, onSelect }: { label: string; value: string; onSelect: (value: string) => void }) => <button onClick={() => onSelect(value)}>{label}</button> }));
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: () => undefined }));
vi.mock("./resources/import-microsoft-emails-modal", () => ({ ImportMicrosoftEmailsModal: () => null }));
vi.mock("./resources/domain-import-modal", () => ({ ImportDomainModal: () => null }));
vi.mock("./resources/import-proto-emails-modal", () => ({ ImportProtoEmailsModal: () => null }));
vi.mock("./resources/import-owned-daum-emails-modal", () => ({ ImportOwnedDaumEmailsModal: () => null }));
vi.mock("./resources/supplier-application-modal", () => ({ SupplierApplicationModal: () => null, ensureSupplierRole: async () => true }));
vi.mock("./system-settings/settings-layout", () => ({
  SettingsSection: ({ children }: { children: ReactNode }) => <>{children}</>, SettingsCardHeader: () => null,
}));
vi.mock("@/lib/inventory-refresh-api", () => ({
  getInventoryRefreshes: async () => ({ items: [{ projectId: 7, projectName: "Seven", status: "scheduled" }], parameters: {} }),
  triggerInventoryRefresh: mocks.inventory,
}));
vi.mock("@/lib/resources-api", async (original) => ({
  ...await original<typeof import("@/lib/resources-api")>(),
  listOwnedMicrosoftResources: mocks.list, listOwnedDomainResources: mocks.list,
  validateMicrosoftResourcesByFilter: mocks.checkMicrosoft, validateDomainResourcesByFilter: mocks.checkDomain,
}));
vi.mock("@/lib/daum-api", async (original) => ({
  ...await original<typeof import("@/lib/daum-api")>(),
  listOwnedDaumResources: mocks.list, validateDaumResourcesByFilter: mocks.checkDaum,
}));
vi.mock("@/lib/proto-api", async (original) => ({
  ...await original<typeof import("@/lib/proto-api")>(),
  listOwnedProtoResources: mocks.list, validateProtoResourcesByFilter: mocks.checkProto,
}));
vi.mock("@/lib/proxies-api", async (original) => ({
  ...await original<typeof import("@/lib/proxies-api")>(),
  getAdminProxyStats: async () => null, checkAdminProxiesByFilter: mocks.checkProxies,
}));
vi.mock("@/lib/admin-kitesim-api", async (original) => ({
  ...await original<typeof import("@/lib/admin-kitesim-api")>(),
  syncAdminKitesimAccount: mocks.sync,
}));

import { usePageConfirmation } from "@/hooks/use-page-confirmation";
import MicrosoftEmails from "./MicrosoftEmails";
import MicrosoftEnterpriseEmails from "./MicrosoftEnterpriseEmails";
import DomainEmails from "./DomainEmails";
import DaumEmails from "./DaumEmails";
import ProtoEmails from "./ProtoEmails";
import ProxyManagement from "./ProxyManagement";
import AdminKitesim from "./AdminKitesim";
import InventoryRefreshSection from "./system-settings/inventory-refresh";

const checks = [
  { name: "Microsoft", Page: MicrosoftEmails, request: mocks.checkMicrosoft },
  { name: "Microsoft enterprise", Page: MicrosoftEnterpriseEmails, request: mocks.checkMicrosoft },
  { name: "Domain", Page: DomainEmails, request: mocks.checkDomain },
  { name: "Daum", Page: DaumEmails, request: mocks.checkDaum },
  { name: "Proto", Page: ProtoEmails, request: mocks.checkProto },
  { name: "Proxy", Page: ProxyManagement, request: mocks.checkProxies },
];
beforeEach(() => {
  vi.clearAllMocks();
  mocks.userId = 1;
  mocks.total = 3;
  mocks.confirm.mockImplementation(() => ({ destroy: vi.fn() }));
  mocks.list.mockResolvedValue({ items: [], total: 3 });
  for (const { request } of checks) request.mockResolvedValue({ requested: 3, queued: 3, items: [] });
  mocks.sync.mockResolvedValue({});
  mocks.inventory.mockResolvedValue([7]);
});
afterEach(() => {
  cleanup();
  document.cookie = "csrf_token=; Max-Age=0; path=/";
});

describe.each(checks)("$name toolbar confirmation", ({ Page, request }) => {
  it("waits for explicit confirmation and keeps the confirmed filter", async () => {
    render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "Normal" }));
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    expect(mocks.confirm).toHaveBeenCalledOnce();
    expect(mocks.confirm.mock.lastCall?.[0]).toMatchObject({ title: "Confirm check all", cancelText: "Cancel" });
    expect(mocks.translate).toHaveBeenCalledWith(expect.stringMatching(/^Confirm check all.*content$/), { count: 3 });
    expect(request).not.toHaveBeenCalled();
    const confirmation = mocks.confirm.mock.lastCall![0];
    fireEvent.click(screen.getByRole("button", { name: "Abnormal" }));
    await act(async () => { await confirmation.onOk(); });
    expect(request).toHaveBeenCalledOnce();
    expect(request.mock.calls[0][0]).toMatchObject({ status: "normal" });
  });
  it("does not submit when the confirmation is canceled", async () => {
    render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    const confirmation = mocks.confirm.mock.lastCall![0];
    await act(async () => { confirmation.onCancel?.(); });
    expect(request).not.toHaveBeenCalled();
  });
  it("closes the confirmation and blocks its callback after leaving the page", async () => {
    const { unmount } = render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    const confirmation = mocks.confirm.mock.lastCall![0];
    const modal = mocks.confirm.mock.results[0].value;
    unmount();
    expect(modal.destroy).toHaveBeenCalledOnce();
    await act(async () => { await confirmation.onOk(); });
    expect(request).not.toHaveBeenCalled();
  });
  it.each([2, null])("blocks the old confirmation when the user becomes %s", async (userId) => {
    const { rerender } = render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    const confirmation = mocks.confirm.mock.lastCall![0];
    const modal = mocks.confirm.mock.results[0].value;
    mocks.userId = userId;
    rerender(<Page />);
    expect(modal.destroy).toHaveBeenCalledOnce();
    await act(async () => { await confirmation.onOk(); });
    expect(request).not.toHaveBeenCalled();
    mocks.userId = 1;
    rerender(<Page />);
    await act(async () => { await confirmation.onOk(); });
    expect(request).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    await act(async () => { await mocks.confirm.mock.lastCall![0].onOk(); });
    expect(request).toHaveBeenCalledOnce();
  });
  it("does not prompt or submit for an empty filter", async () => {
    mocks.total = 0;
    render(<Page />);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "Check" })); });
    expect(mocks.confirm).not.toHaveBeenCalled();
    expect(request).not.toHaveBeenCalled();
  });
  it("blocks the old confirmation if another tab changes the session cookie", async () => {
    document.cookie = "csrf_token=old-session; path=/";
    render(<Page />);
    fireEvent.click(screen.getByRole("button", { name: "Check" }));
    const confirmation = mocks.confirm.mock.lastCall![0];
    document.cookie = "csrf_token=new-session; path=/";
    await act(async () => { await confirmation.onOk(); });
    expect(request).not.toHaveBeenCalled();
  });
});

it("confirms current-page synchronization before queuing each distinct account", async () => {
  render(<AdminKitesim />);
  fireEvent.click(screen.getByRole("button", { name: "Synchronize" }));
  expect(mocks.confirm).toHaveBeenCalledOnce();
  expect(mocks.confirm.mock.lastCall?.[0]).toMatchObject({ title: "Synchronize current page", content: "Confirm synchronize current page content" });
  expect(mocks.sync).not.toHaveBeenCalled();
  expect(mocks.translate).toHaveBeenCalledWith("Confirm synchronize current page content", { count: 2 });
  await act(async () => { await mocks.confirm.mock.lastCall![0].onOk(); });
  expect(mocks.sync.mock.calls).toEqual([[11], [22]]);
});

it("does not synchronize the page when confirmation is canceled", async () => {
  render(<AdminKitesim />);
  fireEvent.click(screen.getByRole("button", { name: "Synchronize" }));
  await act(async () => { mocks.confirm.mock.lastCall![0].onCancel?.(); });
  expect(mocks.sync).not.toHaveBeenCalled();
});

it.each([false, true])("confirms inventory rebuilds before submitting (selected project: %s)", async (selected) => {
  render(<InventoryRefreshSection canWrite />);
  await screen.findByRole("option", { name: "Seven (#7)" });
  if (selected) fireEvent.change(screen.getByRole("combobox", { name: "库存刷新项目" }), { target: { value: "7" } });
  fireEvent.click(screen.getByRole("button", { name: selected ? "刷新所选项目" : "刷新全部" }));
  expect(mocks.confirm).toHaveBeenCalledOnce();
  expect(mocks.inventory).not.toHaveBeenCalled();
  await act(async () => { await mocks.confirm.mock.lastCall![0].onOk(); });
  expect(mocks.inventory).toHaveBeenCalledExactlyOnceWith(selected ? 7 : undefined);
});

it("closes Kitesim synchronization confirmations and rejects their stale callback", async () => {
  const { unmount } = render(<AdminKitesim />);
  fireEvent.click(screen.getByRole("button", { name: "Synchronize" }));
  const confirmation = mocks.confirm.mock.lastCall![0];
  const modal = mocks.confirm.mock.results[0].value;
  unmount();
  expect(modal.destroy).toHaveBeenCalledOnce();
  await act(async () => { await confirmation.onOk(); });
  expect(mocks.sync).not.toHaveBeenCalled();
});

it("blocks a Kitesim synchronization confirmation after switching accounts", async () => {
  const { rerender } = render(<AdminKitesim />);
  fireEvent.click(screen.getByRole("button", { name: "Synchronize" }));
  const confirmation = mocks.confirm.mock.lastCall![0];
  const modal = mocks.confirm.mock.results[0].value;
  mocks.userId = 2;
  rerender(<AdminKitesim />);
  expect(modal.destroy).toHaveBeenCalledOnce();
  await act(async () => { await confirmation.onOk(); });
  expect(mocks.sync).not.toHaveBeenCalled();
});

it.each(["unmount", "switch user"])("blocks inventory refresh confirmations after %s", async (change) => {
  const { unmount, rerender } = render(<InventoryRefreshSection canWrite />);
  await screen.findByRole("option", { name: "Seven (#7)" });
  fireEvent.click(screen.getByRole("button", { name: "刷新全部" }));
  const confirmation = mocks.confirm.mock.lastCall![0];
  const modal = mocks.confirm.mock.results[0].value;
  if (change === "unmount") unmount();
  else {
    mocks.userId = 2;
    rerender(<InventoryRefreshSection canWrite />);
  }
  expect(modal.destroy).toHaveBeenCalledOnce();
  await act(async () => { await confirmation.onOk(); });
  expect(mocks.inventory).not.toHaveBeenCalled();
});

it("keeps the original promise and afterClose handler, and releases closed dialogs", async () => {
  const { result, unmount } = renderHook(usePageConfirmation);
  const pending = Promise.resolve();
  const onOk = vi.fn(() => pending);
  const afterClose = vi.fn();
  act(() => { result.current({ onOk, afterClose }); });
  const confirmation = mocks.confirm.mock.lastCall![0];
  const modal = mocks.confirm.mock.results[0].value;
  expect(confirmation.onOk()).toBe(pending);
  confirmation.afterClose();
  expect(afterClose).toHaveBeenCalledOnce();
  await act(async () => { await confirmation.onOk(); });
  expect(onOk).toHaveBeenCalledOnce();
  unmount();
  expect(modal.destroy).not.toHaveBeenCalled();
});
