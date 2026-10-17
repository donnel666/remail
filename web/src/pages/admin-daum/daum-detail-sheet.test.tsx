// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { IamApiError } from "@/lib/api-client";
import type { AdminDaumResourceDetail } from "./admin-daum-types";

const mocks = vi.hoisted(() => ({
  tasks: vi.fn(), validate: vi.fn(), scan: vi.fn(), fetchMail: vi.fn(), getDetail: vi.fn(),
  mailPanel: vi.fn(), toastError: vi.fn(), translate: (key: string) => key,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Container = ({ children }: any) => <div>{children}</div>;
  const Tabs = ({ children, onChange }: any) => <div role="tablist">{React.Children.map(children, (child: any) => <button role="tab" onClick={() => onChange(child.props.itemKey)}>{child.props.tab}</button>)}</div>;
  Tabs.TabPane = () => null;
  return {
    Button: ({ children, disabled, loading, onClick }: any) => <button disabled={disabled || loading} onClick={onClick}>{children}</button>,
    Empty: ({ description }: any) => <div>{description}</div>,
    SideSheet: ({ children, visible }: any) => visible ? <section>{children}</section> : null,
    Space: Container, Spin: () => <span>Loading</span>, Tag: Container, Tabs,
    Table: ({ dataSource, columns }: any) => <table><thead><tr>{columns.map((column: any) => <th key={column.dataIndex}>{column.title}</th>)}</tr></thead><tbody>{dataSource.map((item: any) => <tr key={item.taskId ?? item.id}>{columns.map((column: any) => <td key={column.dataIndex}>{column.render ? column.render(item[column.dataIndex], item) : item[column.dataIndex]}</td>)}</tr>)}</tbody></table>,
    Typography: { Text: Container }, Toast: { error: mocks.toastError, success: vi.fn() },
  };
});
vi.mock("@/hooks/use-shared-page-size", () => ({ useSharedPageSize: () => [20, vi.fn()] }));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/components/semi/card-pro-pagination", () => ({ createCardProPagination: () => null }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: ({ text }: any) => <span>{text}</span> }));
vi.mock("../admin-microsoft/microsoft-detail-sheet", () => ({ ResourceMailsPanel: (props: unknown) => { mocks.mailPanel(props); return <div data-testid="mail-panel" />; }, ResourceProxyDiagnostics: () => null }));
vi.mock("./use-admin-daum-allocation-page", () => ({ useAdminDaumAllocationPage: () => ({ items: [], total: 0 }) }));
vi.mock("./daum-meta", () => ({
  ALLOCATION_STATUS_META: {}, ConfiguredTag: () => null, DRAWER_PANEL_HEIGHT: 360, DRAWER_TABLE_SCROLL_Y: 220,
  InfoItem: ({ label, value }: any) => <div>{label}: {value}</div>, MAILBOX_META: {}, MAILBOX_TEXT_COLOR: {},
  OwnerIdentity: () => null, SUPPLY_SCOPE_META: {}, formatTime: (value: string) => value,
  renderMailboxTag: (value: string) => value, renderStatusTag: (value: string) => value,
  renderTaskStatusTag: (value: string) => value, taskKindLabel: (value: string) => value,
}));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("@/lib/admin-daum-api", () => ({
  listAdminDaumTasks: mocks.tasks, validateAdminDaumResource: mocks.validate,
  scanAdminDaumProjects: mocks.scan, fetchAdminDaumMail: mocks.fetchMail,
}));
import { DaumDetailSheet } from "./daum-detail-sheet";

const initial: AdminDaumResourceDetail = {
  id: 41, version: 3, ownerUserId: 7, owner: null, email: "user@daum.net", emailAddress: "user@daum.net", suffix: "daum.net",
  proxyBindings: [], recentTasks: [], activeTask: null, status: "validating", forSale: false, longLived: true, qualityScore: 0, protocol: "imap", kakaoEmail: "account@kakao.example",
  bindingAddress: "helper@outlook.com", bindingStatus: "linked", pickupUrlConfigured: true, aliases: [],
  passwordConfigured: true, loginPasswordConfigured: true, appPasswordConfigured: true, credentialRevision: 2,
  credentialUpdatedAt: "2026-10-01T00:00:00Z", validationGeneration: 1, validationFailures: 0,
  createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
  credentials: { passwordConfigured: true, appPasswordConfigured: true, revision: 2 },
};
const page = (status?: string) => ({ items: status ? [{ taskId: "daum:validation:41:1", status }] : [], total: status ? 1 : 0, succeeded: status === "succeeded" ? 1 : 0, offset: 0, limit: 20 });
function Harness({ resource = initial }: { resource?: AdminDaumResourceDetail }) {
  const [detail, setDetail] = useState(resource);
  return <><output data-testid="resource-version">{detail.version}</output><DaumDetailSheet detail={detail} loading={false} busy={false}
    onRefresh={async () => setDetail(await mocks.getDetail(detail.id))}
    onCancel={vi.fn()} onDelete={vi.fn()} onEdit={vi.fn()} onRecover={vi.fn()}
    onReplaceCredentials={vi.fn()} onToggleDisabled={vi.fn()} onTogglePublish={vi.fn()} onMaintain={vi.fn()} /></>;
}
async function settle() { await act(async () => { await vi.advanceTimersByTimeAsync(0); }); }
beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  mocks.tasks.mockResolvedValue(page());
  mocks.validate.mockResolvedValue({});
  mocks.getDetail.mockResolvedValue({ ...initial, status: "normal", version: 4, qualityScore: 100 });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("Daum task detail freshness", () => {
  it("keeps the administrator Microsoft tab order and puts private metadata only in detail", () => {
    const aliases = (["hanmail.net", "hanmail2.net"] as const).map((suffix, index) => ({
      id: index + 1, resourceId: 41, ownerUserId: 1, email: `user@${suffix}`, suffix,
      status: "normal" as const, version: 1, createdAt: "2026-10-01T00:00:00Z", updatedAt: "2026-10-01T00:00:00Z",
    }));
    render(<Harness resource={{ ...initial, aliases }} />);
    expect(within(screen.getByRole("tablist")).getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Basic info", "Orders", "Explicit aliases", "Task details", "Mailbox", "Auxiliary mailbox",
    ]);
    expect(screen.getAllByText("helper@outlook.com")).toHaveLength(1);
    expect(screen.getAllByText("account@kakao.example")).toHaveLength(1);
    expect(screen.getByText("Operational summary")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Explicit aliases" }));
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual(["Explicit alias", "Created at"]);
    expect(screen.getByText("user@hanmail.net")).toBeInTheDocument();
    expect(screen.getByText("user@hanmail2.net")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("tab", { name: "Auxiliary mailbox" }));
    expect(screen.getByText("helper@outlook.com")).toBeInTheDocument();
    expect(screen.queryByText("account@kakao.example")).not.toBeInTheDocument();
    expect(mocks.mailPanel).toHaveBeenLastCalledWith(expect.objectContaining({
      auxiliary: true, extraOffset: 40, hideMailboxMeta: true, fetchEnabled: false, resourceType: "daum", resourceId: 41,
    }));
  });

  it("keeps an accepted maintenance command successful when refreshing the parent fails", async () => {
    mocks.getDetail.mockRejectedValueOnce(new Error("Detail refresh failed"));
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await settle();
    expect(mocks.validate).toHaveBeenCalledTimes(1);
    expect(mocks.toastError).toHaveBeenCalledWith("Admin Daum resources load failed.");
    expect(mocks.toastError).not.toHaveBeenCalledWith("Daum resource operation failed.");
  });

  it.each(["succeeded", "removed"])("refreshes the parent once when an active task is %s and uses the new version", async (status) => {
    mocks.tasks.mockResolvedValueOnce(page("running")).mockResolvedValue(page(status === "removed" ? undefined : status));
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    expect(screen.getByRole("button", { name: "Scan projects" })).toBeDisabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    expect(mocks.getDetail).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Scan projects" })).toBeEnabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(mocks.tasks).toHaveBeenCalledTimes(2);
    expect(mocks.getDetail).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await settle();
    expect(mocks.validate).toHaveBeenCalledWith(41);
    expect(screen.getByTestId("resource-version")).toHaveTextContent("4");
  });

  it.each(["opening", "after_submit"])("refreshes once when the first task read is already terminal (%s)", async (scenario) => {
    if (scenario === "after_submit") {
      mocks.tasks.mockResolvedValueOnce(page()).mockResolvedValue(page("succeeded"));
      mocks.getDetail.mockResolvedValueOnce(initial).mockResolvedValue({ ...initial, status: "normal", version: 4 });
    } else {
      mocks.tasks.mockResolvedValue(page("succeeded"));
    }
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    if (scenario === "after_submit") {
      fireEvent.click(screen.getByRole("button", { name: "Validate" }));
      await settle();
    }
    expect(screen.getByRole("button", { name: "Scan projects" })).toBeEnabled();
    expect(mocks.getDetail).toHaveBeenCalledTimes(scenario === "after_submit" ? 2 : 1);
    await act(async () => { await vi.advanceTimersByTimeAsync(10_000); });
    expect(mocks.getDetail).toHaveBeenCalledTimes(scenario === "after_submit" ? 2 : 1);
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await settle();
    expect(mocks.validate).toHaveBeenLastCalledWith(41);
    expect(screen.getByTestId("resource-version")).toHaveTextContent("4");
  });

  it("refreshes on a version conflict without automatically repeating the command", async () => {
    mocks.validate.mockRejectedValueOnce(new IamApiError(409, { code: "resource_version_conflict", message: "Resource version changed." }));
    render(<Harness resource={{ ...initial, status: "normal" }} />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await settle();
    expect(mocks.getDetail).toHaveBeenCalledTimes(1);
    expect(mocks.validate).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await settle();
    expect(mocks.validate.mock.calls).toEqual([[41], [41]]);
    expect(screen.getByTestId("resource-version")).toHaveTextContent("4");
  });

  it("ignores a terminal task response after the detail sheet is unmounted", async () => {
    let resolve!: (value: ReturnType<typeof page>) => void;
    mocks.tasks.mockResolvedValueOnce(page("running")).mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    const view = render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    const signal = mocks.tasks.mock.calls[1][3] as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => { resolve(page("succeeded")); });
    expect(mocks.getDetail).not.toHaveBeenCalled();
  });

  it("ignores an action completion after switching away from the task tab", async () => {
    let resolve!: (value: object) => void;
    mocks.validate.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    render(<Harness />);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await settle();
    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    fireEvent.click(screen.getByRole("tab", { name: "Basic info" }));
    await act(async () => { resolve({}); });
    expect(mocks.getDetail).not.toHaveBeenCalled();
  });
});
