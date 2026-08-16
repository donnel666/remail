// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  listMessages: vi.fn(),
  listPhones: vi.fn(),
  listTasks: vi.fn(),
  toastError: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@douyinfe/semi-ui", () => {
  const Passthrough = ({ children }: { children?: ReactNode }) => <div>{children}</div>;
  const Tabs = Object.assign(Passthrough, { TabPane: Passthrough });
  const Modal = Object.assign(Passthrough, { confirm: vi.fn(), error: vi.fn() });
  return {
    Button: ({ children, onClick }: { children?: ReactNode; onClick?: () => void }) => (
      <button onClick={onClick} type="button">{children}</button>
    ),
    DatePicker: Passthrough,
    Dropdown: Passthrough,
    Empty: ({ description }: { description?: ReactNode }) => <div>{description}</div>,
    Input: Passthrough,
    Modal,
    Notification: { info: vi.fn(), close: vi.fn() },
    Select: Passthrough,
    SideSheet: Passthrough,
    Space: Passthrough,
    Tabs,
    Tag: Passthrough,
    TextArea: Passthrough,
    Toast: { error: mocks.toastError, success: vi.fn(), warning: vi.fn() },
    Tooltip: Passthrough,
    Typography: { Text: Passthrough },
  };
});

vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@douyinfe/semi-illustrations", () => ({
  IllustrationNoResult: () => null,
  IllustrationNoResultDark: () => null,
}));
vi.mock("lucide-react", () => ({
  SlidersHorizontal: () => null,
  Smartphone: () => null,
}));

vi.mock("@/components/semi/card-table", () => ({
  CardTable: ({ dataSource }: { dataSource: Array<Record<string, unknown>> }) => (
    <div>{dataSource.map((row, index) => <div key={String(row.content ?? row.taskId ?? index)}>{String(row.content ?? row.status ?? "")}</div>)}</div>
  ),
  DESKTOP_TABLE_SCROLL_Y: 480,
}));

vi.mock("@/components/semi/copyable-table-text", () => ({
  CopyableTableText: ({ text }: { text: string }) => <span>{text}</span>,
}));

vi.mock("@/lib/admin-kitesim-api", () => ({
  deleteAdminKitesimPhones: vi.fn(),
  disableAdminKitesimPhones: vi.fn(),
  enableAdminKitesimPhones: vi.fn(),
  importAdminKitesimAccounts: vi.fn(),
  listAdminKitesimAccountTasks: mocks.listTasks,
  listAdminKitesimMessages: mocks.listMessages,
  listAdminKitesimPhones: mocks.listPhones,
  syncAdminKitesimAccount: vi.fn(),
}));

vi.mock("./admin-microsoft/microsoft-detail-sheet", () => ({
  ServerPaginatedDrawerTable: ({ dataSource }: { dataSource: Array<Record<string, unknown>> }) => (
    <div>{dataSource.map((row) => <div key={String(row.taskId)}>{String(row.status)}</div>)}</div>
  ),
}));

import type { AdminKitesimPhoneItem } from "@/lib/admin-kitesim-api";
import { KitesimMessagesPanel, KitesimTaskDiagnostics } from "./AdminKitesim";

function phone(phoneId: number, phoneNumber: string) {
  return { phoneId, phoneNumber } as AdminKitesimPhoneItem;
}

describe("KitesimMessagesPanel", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  it("does not retain the previous phone messages when the next load fails", async () => {
    mocks.listMessages
      .mockResolvedValueOnce([{ caller: "10086", content: "old code 123456", time: "2026-08-16 10:00:00" }])
      .mockRejectedValueOnce(new Error("network failed"));

    const view = render(<KitesimMessagesPanel item={phone(1, "+1 555 0001")} />);
    await screen.findByText("old code 123456");

    view.rerender(<KitesimMessagesPanel item={phone(2, "+1 555 0002")} />);

    await waitFor(() => expect(mocks.listMessages).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(mocks.toastError).toHaveBeenCalledTimes(1));
    expect(screen.queryByText("old code 123456")).not.toBeInTheDocument();
  });

  it("clears the previous phone messages when switching to an unsynced account", async () => {
    mocks.listMessages.mockResolvedValueOnce([
      { caller: "10086", content: "old code 123456", time: "2026-08-16 10:00:00" },
    ]);

    const view = render(<KitesimMessagesPanel item={phone(1, "+1 555 0001")} />);
    await screen.findByText("old code 123456");

    view.rerender(<KitesimMessagesPanel item={phone(0, "")} />);

    expect(screen.queryByText("old code 123456")).not.toBeInTheDocument();
    expect(screen.getByText("No phone number synchronized")).toBeInTheDocument();
    expect(mocks.listMessages).toHaveBeenCalledTimes(1);
  });
});

describe("KitesimTaskDiagnostics", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(cleanup);

  const task = (status: AdminKitesimPhoneItem["syncStatus"]) => ({
    account: "owner@example.com",
    accountId: 42,
    createdAt: "2026-08-16T00:00:00Z",
    phoneId: 7,
    phoneNumber: "+1 4165550001",
    status: "active",
    autoRenew: false,
    tokenAvailable: true,
    syncHealthy: status === "succeeded",
    syncStatus: status,
    syncAttempts: 1,
    syncQueuedAt: "2026-08-16T00:00:00Z",
  }) as AdminKitesimPhoneItem;

  const taskList = (status: AdminKitesimPhoneItem["syncStatus"]) => ({
    items: [{
      attempts: 1,
      queuedAt: "2026-08-16T00:00:00Z",
      status,
      taskId: "kitesim-sync:1",
      updatedAt: "2026-08-16T00:00:01Z",
    }],
    limit: 20,
    offset: 0,
    succeeded: status === "succeeded" ? 1 : 0,
    total: 1,
  });

  it("polls only the task detail until synchronization reaches a terminal state", async () => {
    vi.useFakeTimers();
    try {
      mocks.listTasks
        .mockResolvedValueOnce(taskList("running"))
        .mockResolvedValueOnce(taskList("succeeded"));

      render(<KitesimTaskDiagnostics item={task("queued")} />);
      await act(async () => {
        await Promise.resolve();
      });
      expect(mocks.listTasks).toHaveBeenCalledTimes(1);
      expect(mocks.listPhones).not.toHaveBeenCalled();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_500);
      });
      expect(mocks.listTasks).toHaveBeenCalledTimes(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_500);
      });
      expect(mocks.listTasks).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
