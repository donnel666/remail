// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

import type { AdminUser, AdminUserRole } from "./admin-users/admin-users-api";

const mocks = vi.hoisted(() => ({
  currentUser: {
    role: "admin" as AdminUserRole,
    permissions: ["iam:user:write", "iam:permission:sensitive"],
  },
  refresh: vi.fn(),
  translate: (key: string) => key,
  updateLoadedItems: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: mocks.translate }),
}));
vi.mock("@douyinfe/semi-ui", () => {
  const Passthrough = ({ children }: { children?: ReactNode }) => <>{children}</>;
  return {
    Avatar: Passthrough, DatePicker: Passthrough, Dropdown: Passthrough,
    Empty: Passthrough, Input: Passthrough, Pagination: Passthrough,
    Space: Passthrough, Tag: Passthrough, Tooltip: Passthrough,
    Tabs: Object.assign(Passthrough, { TabPane: Passthrough }),
    Modal: Object.assign(Passthrough, { confirm: vi.fn() }),
    Toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
    Typography: { Text: Passthrough },
    Button: ({ children, disabled, onClick }: {
      children?: ReactNode; disabled?: boolean; onClick?: () => void;
    }) => <button disabled={disabled} onClick={onClick} type="button">{children}</button>,
  };
});
vi.mock("@/context/auth-provider", () => ({
  useAuth: () => ({ currentUser: mocks.currentUser }),
}));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-block-paged-list", () => ({
  useBlockPagedList: () => ({
    loading: false, pagedItems: [target], total: 1,
    refresh: mocks.refresh, updateLoadedItems: mocks.updateLoadedItems,
  }),
}));
vi.mock("./resources/use-selection-notification", () => ({ useSelectionNotification: () => {} }));
vi.mock("./admin-users/use-user-groups", () => ({ useUserGroups: () => ({ groups: [] }) }));
vi.mock("./admin-users/create-user-modal", () => ({
  CreateUserModal: () => null, EditUserModal: () => null,
}));
vi.mock("./admin-users/user-detail-sheet", () => ({
  UserDetailSheet: () => null, WalletAdjustModal: () => null,
}));
vi.mock("@/components/semi/card-pro", () => ({
  CardPro: ({ children }: { children?: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/semi/card-table", () => ({
  DESKTOP_TABLE_SCROLL_Y: 480,
  CardTable: ({ columns, dataSource }: {
    columns: Array<{ key: string; render: (value: unknown, user: AdminUser) => ReactNode }>;
    dataSource: AdminUser[];
  }) => <>{dataSource.map((user) => (
    <div key={user.id}>{columns.find((column) => column.key === "operate")?.render(undefined, user)}</div>
  ))}</>,
}));

import AdminUsers from "./AdminUsers";

const target: AdminUser = {
  id: 99, email: "root@example.com", nickname: "Root", role: "super_admin",
  userGroup: { id: 1, code: "normal", name: "Normal", description: "", enabled: true },
  hasLocalPassword: true, enabled: true, consumerBalance: "0",
  createdAt: "2026-09-11T00:00:00Z", updatedAt: "2026-09-11T00:00:00Z",
};

afterEach(cleanup);

it("updates protected row editing when the operator's role or sensitive permission changes", () => {
  const view = render(<AdminUsers />);
  expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();

  mocks.currentUser = { ...mocks.currentUser, role: "super_admin" };
  view.rerender(<AdminUsers />);
  expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();

  mocks.currentUser = { ...mocks.currentUser, role: "admin" };
  view.rerender(<AdminUsers />);
  expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();

  mocks.currentUser = { ...mocks.currentUser, role: "super_admin" };
  view.rerender(<AdminUsers />);
  expect(screen.getByRole("button", { name: "Edit" })).toBeEnabled();

  mocks.currentUser = { ...mocks.currentUser, permissions: ["iam:user:write"] };
  view.rerender(<AdminUsers />);
  expect(screen.getByRole("button", { name: "Edit" })).toBeDisabled();
});
