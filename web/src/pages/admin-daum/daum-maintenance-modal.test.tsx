// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { AdminDaumResourceItem } from "./admin-daum-types";
import { createDaumBulkSession } from "@/lib/daum-bulk-session";

const mocks = vi.hoisted(() => ({
  tasks: vi.fn(), validate: vi.fn(), scan: vi.fn(), bulk: vi.fn(), error: vi.fn(), success: vi.fn(), warning: vi.fn(),
  translate: (key: string) => key,
}));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Modal: ({ children, onOk, okText, title, visible, width, confirmLoading, okButtonProps }: any) => visible ? <section role="dialog" aria-label={title} data-width={width}>{children}<button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button></section> : null,
  Tag: ({ children }: any) => <span>{children}</span>, Spin: () => null,
  Toast: { error: mocks.error, success: mocks.success, warning: mocks.warning },
}));
vi.mock("@/lib/admin-daum-api", () => ({
  listAdminDaumTasks: mocks.tasks, validateAdminDaumResource: mocks.validate, scanAdminDaumProjects: mocks.scan,
  maintainAdminDaumResourcesByIds: mocks.bulk, maintainAdminDaumResourcesByFilter: mocks.bulk,
}));
vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("./daum-meta", () => ({ TASK_STATUS_META: { succeeded: { color: "green", label: "Succeeded" } }, formatTime: () => "" }));
import { DaumMaintenanceModal } from "./daum-maintenance-modal";
import { DaumBulkMaintenanceModal } from "./daum-bulk-maintenance-modal";

const resource = {
  id: 41, status: "normal", emailAddress: "user@daum.net", appPasswordConfigured: true,
} as AdminDaumResourceItem;
beforeEach(() => {
  vi.clearAllMocks();
  mocks.tasks.mockResolvedValue({ items: [] });
  mocks.validate.mockResolvedValue({ queued: true });
  mocks.bulk.mockResolvedValue({ requested: 2, taskId: "daum_bulk:7", status: "queued", affected: 0, skipped: 0, reasonCounts: [] });
});
afterEach(cleanup);

it("keeps the Microsoft maintenance dialog width and only offers available Daum operations", async () => {
  render(<DaumMaintenanceModal target={{ ...resource, appPasswordConfigured: false }} onCancel={vi.fn()} onCompleted={vi.fn()} />);
  expect(screen.getByRole("dialog")).toHaveAttribute("data-width", "680");
  expect(screen.getByRole("button", { name: /Validate resource/ })).toBeEnabled();
  expect(screen.getByRole("button", { name: /Scan projects/ })).toBeDisabled();
  expect(screen.queryByRole("button", { name: /Create alias|Update RT/ })).not.toBeInTheDocument();
  await waitFor(() => expect(mocks.tasks).toHaveBeenCalledOnce());
});

it.each(["single", "bulk"])("closes accepted %s maintenance after a refresh failure without resubmitting", async (kind) => {
  const onCancel = vi.fn();
  const onCompleted = vi.fn().mockRejectedValue(new Error("Refresh failed"));
  render(kind === "single"
    ? <DaumMaintenanceModal target={resource} onCancel={onCancel} onCompleted={onCompleted} />
    : <DaumBulkMaintenanceModal target={{ count: 2, mode: "ids", resourceIds: [41, 42] }} session={createDaumBulkSession()} onCancel={onCancel} onCompleted={onCompleted} />);
  fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));
  await waitFor(() => expect(onCancel).toHaveBeenCalledOnce());
  expect(onCompleted).toHaveBeenCalledOnce();
  expect(kind === "single" ? mocks.validate : mocks.bulk).toHaveBeenCalledOnce();
  expect(mocks.success).toHaveBeenCalled();
  expect(mocks.error).toHaveBeenCalledWith("Admin Daum resources load failed.");
  expect(mocks.error).not.toHaveBeenCalledWith("Daum resource operation failed.");
});

it("reports a failed maintenance receipt with partial progress instead of success", async () => {
  const result = { taskId: "daum_bulk:7", status: "failed", requested: 10, processed: 4, affected: 3, skipped: 1, reasonCounts: [{ reason: "invalid_state", count: 1 }] };
  mocks.bulk.mockResolvedValue(result);
  const session = createDaumBulkSession();
  render(<DaumBulkMaintenanceModal target={{ count: 10, mode: "filter", filter: {} }} session={session} onCancel={vi.fn()} onCompleted={vi.fn()} />);
  fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));
  await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Daum resource operation failed. Processed: 4/10 · Succeeded: 3 · Skipped: 1 · Reason: invalid_state: 1"));
  expect(mocks.success).not.toHaveBeenCalled();
  expect(mocks.bulk).toHaveBeenCalledWith("validate", {}, session);
});
