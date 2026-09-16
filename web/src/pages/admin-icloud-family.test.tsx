// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ get: vi.fn(), refresh: vi.fn(), translate: (key: string) => key }));
vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate, i18n: { resolvedLanguage: "en" } }) }));
vi.mock("@/lib/admin-icloud-api", () => ({ getAdminICloudFamily: mocks.get, refreshAdminICloudFamily: mocks.refresh }));
vi.mock("@/components/semi/copyable-table-text", () => ({ CopyableTableText: ({ text }: { text: string }) => <span>{text}</span> }));
vi.mock("@douyinfe/semi-ui", () => {
  const Text = ({ children }: { children?: ReactNode }) => <span>{children}</span>;
  return {
    Typography: { Text }, Tag: Text,
    Button: ({ children, disabled, onClick }: { children?: ReactNode; disabled?: boolean; onClick?: () => void }) => <button disabled={disabled} onClick={onClick}>{children}</button>,
    Table: ({ columns, dataSource }: { columns: Array<{title: string; dataIndex: string; render: (value: unknown, row: Record<string, unknown>) => ReactNode}>; dataSource: Array<Record<string, unknown>> }) => <table>
      <thead><tr>{columns.map(column => <th key={column.dataIndex}>{column.title}</th>)}</tr></thead>
      <tbody>{dataSource.map(row => <tr key={String(row.key)}>{columns.map(column => <td key={column.dataIndex}>{column.render(row[column.dataIndex], row)}</td>)}</tr>)}</tbody>
    </table>,
  };
});

import { ICloudFamilyPanel } from "./admin-icloud-family";

const family = {
  state: "ready", familyId: "family", canRefresh: true, unavailableReason: "", lastError: "",
  syncedAt: "2026-09-16T00:00:00Z", importedCount: 2, fullCount: 1, aliasLimit: 750,
  members: [
    { email: "full@example.com", aliasCount: 750, resourceId: 1, organizer: true, current: true },
    { email: "empty@example.com", aliasCount: 0, resourceId: 2, organizer: false, current: false },
    { email: "external@example.com", aliasCount: null, resourceId: null, organizer: false, current: false },
  ],
};

describe("iCloud family details", () => {
  beforeEach(() => { vi.clearAllMocks(); mocks.get.mockResolvedValue(family); mocks.refresh.mockResolvedValue(family); });
  afterEach(cleanup);

  it("automatically refreshes and distinguishes an unimported member from zero aliases", async () => {
    render(<ICloudFamilyPanel resourceId={1} canOperate />);
    await waitFor(() => expect(mocks.refresh).toHaveBeenCalledWith(1, expect.any(AbortSignal)));
    const full = screen.getByText("full@example.com").closest("tr")!;
    const zero = screen.getByText("empty@example.com").closest("tr")!;
    const absent = screen.getByText("external@example.com").closest("tr")!;
    expect(within(full).getByText("750 / 750")).toBeInTheDocument();
    expect(within(zero).getByText("0 / 750")).toBeInTheDocument();
    expect(within(absent).getAllByRole("cell")[1]).toBeEmptyDOMElement();
    expect(screen.getAllByRole("columnheader")).toHaveLength(2);
  });

  it("does not start login without Cookie or a device and preserves cached members on failure", async () => {
    mocks.get.mockResolvedValue({ ...family, canRefresh: false, state: "failed", unavailableReason: "Update Cookie or bind a device to refresh the family.", lastError: "Family refresh failed. Please retry." });
    render(<ICloudFamilyPanel resourceId={1} canOperate />);
    expect(await screen.findByText("full@example.com")).toBeInTheDocument();
    expect(mocks.refresh).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Refresh family" })).toBeDisabled();
    expect(screen.getByRole("alert")).toHaveTextContent("Family refresh failed. Please retry.");
  });

  it("keeps read-only access from starting a refresh", async () => {
    render(<ICloudFamilyPanel resourceId={1} canOperate={false} />);
    expect(await screen.findByText("full@example.com")).toBeInTheDocument();
    expect(mocks.refresh).not.toHaveBeenCalled();
  });

  it("aborts a pending request when the detail is closed", async () => {
    mocks.get.mockReturnValue(new Promise(() => {}));
    const view = render(<ICloudFamilyPanel resourceId={1} canOperate />);
    const signal = mocks.get.mock.calls[0][1] as AbortSignal;
    view.unmount();
    expect(signal.aborted).toBe(true);
    expect(mocks.refresh).not.toHaveBeenCalled();
  });
});

describe("family polling recovery", () => {
  beforeEach(() => { vi.clearAllMocks(); vi.useFakeTimers(); });
  afterEach(() => { cleanup(); vi.useRealTimers(); });

  it("retries a transient polling error and clears the pending state", async () => {
    const pending = { ...family, state: "querying" };
    mocks.get.mockResolvedValueOnce(pending).mockRejectedValueOnce(new Error("temporary network failure")).mockResolvedValue(family);
    mocks.refresh.mockResolvedValue(pending);
    render(<ICloudFamilyPanel resourceId={1} canOperate />);
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh family" })).toBeEnabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(mocks.get).toHaveBeenCalledTimes(3);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Refresh family" })).toBeEnabled();
    await act(async () => { await vi.advanceTimersByTimeAsync(30000); });
    expect(mocks.get).toHaveBeenCalledTimes(3);
  });

  it("checks a failed refresh response without replaying the POST", async () => {
    mocks.get.mockResolvedValue(family);
    mocks.refresh.mockRejectedValueOnce(new Error("response lost after enqueue"));
    render(<ICloudFamilyPanel resourceId={1} canOperate />);
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(mocks.get).toHaveBeenCalledTimes(2);
    expect(mocks.refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("cancels the delayed retry when the panel closes", async () => {
    mocks.get.mockRejectedValue(new Error("offline"));
    const view = render(<ICloudFamilyPanel resourceId={1} canOperate />);
    await act(async () => {});
    view.unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
    expect(mocks.get).toHaveBeenCalledTimes(1);
  });
});
