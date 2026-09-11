// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminUser } from "./admin-users-api";

const mocks = vi.hoisted(() => ({ updateUser: vi.fn(), listGroups: vi.fn() }));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));
vi.mock("./admin-users-api", () => ({
  createAdminUser: vi.fn(),
  updateAdminUser: mocks.updateUser,
  listUserGroups: mocks.listGroups,
}));
vi.mock("@douyinfe/semi-ui", () => {
  const Select = ({ children, disabled, onChange, value }: any) => (
    <select disabled={disabled} onChange={(event) => onChange(event.target.value)} value={value}>
      {children}
    </select>
  );
  Select.Option = ({ children, value }: any) => <option value={value}>{children}</option>;
  return {
    Input: ({ disabled, onChange, value }: any) => (
      <input disabled={disabled} onChange={(event) => onChange(event.target.value)} value={value} />
    ),
    Modal: ({ children, confirmLoading, okButtonProps, okText, onOk, visible }: any) => visible ? (
      <div role="dialog">
        {children}
        <button disabled={confirmLoading || okButtonProps?.disabled} onClick={onOk}>{okText}</button>
      </div>
    ) : null,
    Select,
    Switch: ({ checked, disabled, onChange }: any) => (
      <input checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} role="switch" type="checkbox" />
    ),
    Tag: ({ children }: any) => <span>{children}</span>,
    Toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  };
});

import { EditUserModal } from "./create-user-modal";

const normal = { id: 1, code: "normal", name: "Normal", description: "", enabled: true };
const vip3 = { ...normal, id: 4, code: "vip3", name: "VIP3" };
const target: AdminUser = {
  id: 1, email: "root@example.com", nickname: "Root", role: "super_admin",
  userGroup: normal, hasLocalPassword: true, enabled: true,
  createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z", consumerBalance: "0",
};

describe("protected user group editing", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listGroups.mockResolvedValue([normal, vip3]);
    mocks.updateUser.mockResolvedValue({ ...target, userGroup: vip3 });
  });
  afterEach(cleanup);

  it("changes the group with super administrator group access and submits no identity fields", async () => {
    const onSaved = vi.fn();
    render(<EditUserModal canAssignSuperAdmin canEditSuperAdminGroup onClose={vi.fn()} onSaved={onSaved} user={target} />);
    await screen.findByRole("option", { name: "VIP3" });

    expect(screen.getByLabelText("Email *")).toBeDisabled();
    expect(screen.getByLabelText("Nickname")).toBeDisabled();
    expect(screen.getByLabelText(/^New password/)).toBeDisabled();
    expect(screen.getByLabelText("Role")).toBeDisabled();
    expect(screen.getByRole("switch")).toBeDisabled();
    expect(screen.getByLabelText("User Group")).toBeEnabled();
    fireEvent.change(screen.getByLabelText("User Group"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith({ ...target, userGroup: vip3 }));
    expect(mocks.updateUser).toHaveBeenCalledExactlyOnceWith(1, { userGroupId: 4 });
  });

  it("disables protected group editing even with sensitive permission but without super administrator group access", async () => {
    render(<EditUserModal canAssignSuperAdmin canEditSuperAdminGroup={false} onClose={vi.fn()} onSaved={vi.fn()} user={target} />);
    await screen.findByRole("option", { name: "VIP3" });
    expect(screen.getByLabelText("User Group")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(mocks.updateUser).not.toHaveBeenCalled();
  });
});
