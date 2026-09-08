// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";

const mocks = vi.hoisted(() => ({ importResources: vi.fn(), success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn(), turnstile: vi.fn() }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: { id: 31 } }) }));
vi.mock("@/components/auth/TurnstileGate", () => ({ requireTurnstile: mocks.turnstile }));
vi.mock("@/components/semi/admin-user-select", () => ({ AdminUserSelect: () => null }));
vi.mock("@/lib/proto-api", () => ({
  importProtoResources: mocks.importResources,
  getProtoResourceImport: vi.fn(),
  waitForResourceImport: vi.fn(),
}));
vi.mock("@/lib/admin-proto-api", () => ({
  importAdminProtoResources: vi.fn(), getAdminProtoResourceImport: vi.fn(), listAdminProtoOwners: vi.fn(), waitForAdminProtoResourceImport: vi.fn(),
}));
vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, error: Error) => error.message,
  getApiErrorBodyMessage: () => "warning",
}));
vi.mock("./proto-import-result-panel", () => ({ ProtoImportResultPanel: () => <div>Import results</div> }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick, disabled }: { children: ReactNode; onClick?: () => void; disabled?: boolean }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
  Modal: ({ children, footer, visible }: { children: ReactNode; footer: ReactNode; visible: boolean }) => visible ? <div>{children}{footer}</div> : null,
  Space: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  TextArea: ({ value, onChange }: { value: string; onChange: (value: string) => void }) => <textarea value={value} onChange={(event) => onChange(event.target.value)} />,
  Typography: { Text: ({ children }: { children: ReactNode }) => <span>{children}</span> },
  Toast: { success: mocks.success, error: mocks.error, warning: mocks.warning, info: mocks.info },
}));
import { ImportProtoEmailsModal } from "./import-proto-emails-modal";

describe("Proto import feedback", () => {
  beforeEach(() => { vi.clearAllMocks(); sessionStorage.clear(); mocks.turnstile.mockResolvedValue("challenge"); });
  afterEach(cleanup);
  it("does not report success when the accepted import has failed", async () => {
    mocks.importResources.mockResolvedValue({ importId: 7, status: "failed", lastSafeError: "Invalid proto import format.", imported: 0, skipped: 0, failed: 1 });
    const onSuccess = vi.fn();
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={onSuccess} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Invalid proto import format."));
    expect(mocks.turnstile.mock.calls).toEqual([["proto_resource_import"]]);
    expect(onSuccess).not.toHaveBeenCalled();
    expect(mocks.success.mock.calls).toEqual([["Resource import accepted."]]);
  });
  it("rejects malformed input before verification or upload", async () => {
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={vi.fn()} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret----unexpected" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalled());
    expect(mocks.turnstile).not.toHaveBeenCalled();
    expect(mocks.importResources).not.toHaveBeenCalled();
  });
  it("refreshes committed rows when a later import chunk fails", async () => {
    mocks.importResources.mockResolvedValue({ importId: 8, status: "failed", lastSafeError: "Import interrupted.", imported: 1, skipped: 0, failed: 1 });
    const onSuccess = vi.fn();
    render(<ImportProtoEmailsModal open onOpenChange={vi.fn()} onSuccess={onSuccess} />);
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "person@example.com----secret" } });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await waitFor(() => expect(mocks.error).toHaveBeenCalledWith("Import interrupted."));
    expect(onSuccess).toHaveBeenCalledTimes(1);
  });
});
