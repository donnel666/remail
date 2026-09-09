// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { AdminProtoResourceItem } from "./admin-proto-types";

const mocks = vi.hoisted(() => ({
  listTasks: vi.fn(),
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
  Modal: ({ children, onCancel, onOk, okButtonProps, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        <button onClick={onCancel} type="button">Cancel</button>
        <button disabled={okButtonProps?.disabled} onClick={onOk} type="button">{okText}</button>
      </section>
    ) : null,
  Spin: () => <span>loading</span>,
  Tag: ({ children }: any) => <span>{children}</span>,
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
  Tooltip: ({ children }: any) => <>{children}</>,
  Typography: { Text: ({ children }: any) => <span>{children}</span> },
}));

vi.mock("@/lib/admin-proto-api", () => ({
  listAdminProtoTasks: mocks.listTasks,
  scanAdminProtoProjects: mocks.scanProjects,
  validateAdminProtoResource: mocks.validate,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

import { ProtoMaintenanceModal } from "./proto-maintenance-modal";

const target: AdminProtoResourceItem = {
  id: 41,
  version: 3,
  ownerUserId: 1,
  owner: null,
  email: "maintain@proton.me",
  emailAddress: "maintain@proton.me",
  suffix: "@proton.me",
  longLived: true,
  qualityScore: 0,
  status: "validation_failed",
  forSale: true,
  passwordConfigured: true,
  credentialRevision: 2,
  credentialUpdatedAt: "2026-09-09T00:00:00Z",
  validationGeneration: 2,
  validationFailures: 1,
  createdAt: "2026-09-09T00:00:00Z",
  updatedAt: "2026-09-09T00:01:00Z",
};

describe("ProtoMaintenanceModal", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("shows failed validation and permits retry while tasks are pending or fail to load", async () => {
    const network = vi.fn(() => { throw new Error("Unexpected network request"); });
    vi.stubGlobal("fetch", network);
    let rejectTasks!: (error: Error) => void;
    mocks.listTasks.mockReturnValue(new Promise((_resolve, reject) => { rejectTasks = reject; }));
    mocks.validate.mockResolvedValue({});
    const onCancel = vi.fn();
    const onCompleted = vi.fn().mockResolvedValue(undefined);

    render(<ProtoMaintenanceModal onCancel={onCancel} onCompleted={onCompleted} target={target} />);

    const validation = screen.getByRole("button", { name: /Validate resource/ });
    expect(within(validation).getByText("Failed")).toBeInTheDocument();
    expect(validation).toBeEnabled();
    expect(screen.getByRole("button", { name: "Submit maintenance task" })).toBeEnabled();
    expect(mocks.listTasks).toHaveBeenCalledWith(41, 0, 100, expect.any(AbortSignal));

    await act(async () => { rejectTasks(new Error("Task listing unavailable")); });

    expect(mocks.toastError).toHaveBeenCalledWith("Proto task load failed.");
    expect(within(validation).getByText("Failed")).toBeInTheDocument();
    expect(screen.queryByText("Unavailable")).not.toBeInTheDocument();
    expect(validation).toBeEnabled();
    expect(screen.getByRole("button", { name: "Submit maintenance task" })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));
    await waitFor(() => expect(onCancel).toHaveBeenCalledTimes(1));
    expect(mocks.validate).toHaveBeenCalledExactlyOnceWith(41, 3);
    expect(onCompleted).toHaveBeenCalledTimes(1);
    expect(mocks.scanProjects).not.toHaveBeenCalled();
    expect(network).not.toHaveBeenCalled();
  });
});
