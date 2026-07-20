// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminMicrosoftResourceItem } from "./admin-microsoft-types";

const mocks = vi.hoisted(() => ({
  createAlias: vi.fn(),
  listTasks: vi.fn(),
  refreshToken: vi.fn(),
  scanProjects: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  translate: (key: string) => key,
  validate: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Avatar: ({ children }: any) => <span>{children}</span>,
  Modal: ({ children, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        <button onClick={onCancel} type="button">Cancel</button>
        <button onClick={onOk} type="button">{okText}</button>
      </section>
    ) : null,
  Spin: () => <span>loading</span>,
  Tag: ({ children }: any) => <span>{children}</span>,
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
  Tooltip: ({ children }: any) => <>{children}</>,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
}));

vi.mock("@/lib/admin-microsoft-api", () => ({
  createAdminMicrosoftExplicitAlias: mocks.createAlias,
  listAdminMicrosoftTasks: mocks.listTasks,
  refreshAdminMicrosoftToken: mocks.refreshToken,
  scanAdminMicrosoftProjects: mocks.scanProjects,
  validateAdminMicrosoftResource: mocks.validate,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

import { MicrosoftMaintenanceModal } from "./microsoft-maintenance-modal";

const target: AdminMicrosoftResourceItem = {
  activeTask: null,
  bindingAddress: null,
  createdAt: "2026-07-17T00:00:00Z",
  emailAddress: "maintain@outlook.com",
  forSale: true,
  graphAvailable: true,
  id: 41,
  lastAllocatedAt: null,
  lastSafeError: null,
  longLived: true,
  mailProtocol: "graph",
  owner: {
    email: "admin@example.com",
    enabled: true,
    groupName: "Admin",
    id: 1,
    nickname: "Admin",
    role: "super_admin",
  },
  qualityScore: 90,
  rtExpireAt: "2026-09-01T00:00:00Z",
  status: "normal",
  suffix: "@outlook.com",
  tokenHealth: "valid",
  type: "microsoft",
  updatedAt: "2026-07-17T01:00:00Z",
  version: 3,
};

describe("MicrosoftMaintenanceModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listTasks.mockResolvedValue({
      items: [{
        attempts: 1,
        bizId: 41,
        bizType: "microsoft_resource",
        credentialRevision: 3,
        finishedAt: null,
        kind: "history",
        maxAttempts: 3,
        progress: null,
        queuedAt: "2026-07-17T02:00:00Z",
        remainingAttempts: 2,
        startedAt: "2026-07-17T02:01:00Z",
        status: "running",
        taskId: "fetch:9",
        updatedAt: "2026-07-17T02:01:00Z",
      }],
      limit: 100,
      offset: 0,
      succeeded: 0,
      total: 1,
    });
    mocks.scanProjects.mockResolvedValue({});
  });

  afterEach(() => cleanup());

  it("shows four maintenance choices and submits project scanning", async () => {
    const onCancel = vi.fn();
    const onCompleted = vi.fn().mockResolvedValue(undefined);
    render(
      <MicrosoftMaintenanceModal
        onCancel={onCancel}
        onCompleted={onCompleted}
        target={target}
      />
    );

    expect(screen.getByRole("button", { name: /Validate resource/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Create alias/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Scan projects/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Update RT/ })).toBeEnabled();
    await waitFor(() => expect(screen.getByText("Running")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: /Scan projects/ }));
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => expect(mocks.scanProjects).toHaveBeenCalledWith(41));
    expect(onCompleted).toHaveBeenCalledTimes(1);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Project history scan submitted.");
  });

  it("defaults identifying resources to project scanning and allows resubmission", async () => {
    render(
      <MicrosoftMaintenanceModal
        onCancel={vi.fn()}
        onCompleted={vi.fn()}
        target={{ ...target, status: "identifying" }}
      />
    );

    expect(screen.getByRole("button", { name: /Scan projects/ })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => expect(mocks.scanProjects).toHaveBeenCalledWith(41));
  });
});
