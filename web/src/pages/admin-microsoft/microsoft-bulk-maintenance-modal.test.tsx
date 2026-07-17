// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  maintainByFilter: vi.fn(),
  maintainByIds: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => ({
  Modal: ({ children, onCancel, onOk, okText, title, visible }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        <button onClick={onCancel} type="button">Cancel</button>
        <button onClick={onOk} type="button">{okText}</button>
      </section>
    ) : null,
  Tag: ({ children }: any) => <span>{children}</span>,
  Toast: { error: mocks.toastError, success: mocks.toastSuccess },
}));

vi.mock("@/lib/admin-microsoft-api", () => ({
  maintainAdminMicrosoftResourcesByFilter: mocks.maintainByFilter,
  maintainAdminMicrosoftResourcesByIds: mocks.maintainByIds,
}));

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback,
}));

import { MicrosoftBulkMaintenanceModal } from "./microsoft-bulk-maintenance-modal";

describe("MicrosoftBulkMaintenanceModal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.maintainByIds.mockResolvedValue({ accepted: 2 });
  });

  afterEach(() => cleanup());

  it("shows all four actions and submits one ids-mode project scan batch", async () => {
    const onCancel = vi.fn();
    const onCompleted = vi.fn().mockResolvedValue(undefined);
    render(
      <MicrosoftBulkMaintenanceModal
        onCancel={onCancel}
        onCompleted={onCompleted}
        target={{ count: 2, mode: "ids", resourceIds: [41, 42] }}
      />
    );

    expect(screen.getByRole("button", { name: /Validate resource/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Create alias/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Scan projects/ })).toBeEnabled();
    expect(screen.getByRole("button", { name: /Update RT/ })).toBeEnabled();

    fireEvent.click(screen.getByRole("button", { name: /Scan projects/ }));
    fireEvent.click(screen.getByRole("button", { name: "Submit maintenance task" }));

    await waitFor(() => {
      expect(mocks.maintainByIds).toHaveBeenCalledWith("history", [41, 42]);
    });
    expect(mocks.maintainByFilter).not.toHaveBeenCalled();
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Project scan batch submitted.");
    expect(onCompleted).toHaveBeenCalledTimes(1);
    expect(onCancel).toHaveBeenCalledTimes(1);
  });
});
