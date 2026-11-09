// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { ProjectInventoryTotalResponse, ProjectItem } from "@/lib/projects-api";
import Dashboard from "../Dashboard";

const mocks = vi.hoisted(() => ({
  list: vi.fn(), inventory: vi.fn(), orders: vi.fn(), toast: vi.fn(),
  translate: (key: string) => key,
}));

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: mocks.translate }) }));
vi.mock("@/i18n/config", () => ({ default: { resolvedLanguage: "en" } }));
vi.mock("@/context/auth-provider", () => ({ useAuth: () => ({ currentUser: undefined }) }));
vi.mock("@/lib/projects-api", () => ({ listProjects: mocks.list, getProjectInventory: mocks.inventory }));
vi.mock("@/lib/orders-api", () => ({ listOrders: mocks.orders, createOrder: vi.fn(), createOrderBatch: vi.fn(), getOrder: vi.fn() }));
vi.mock("@/lib/mailmatch-api", () => ({ readPickupMail: vi.fn(), readPickupMailBatch: vi.fn(), readPickupMessage: vi.fn() }));
vi.mock("../apply-project-modal", () => ({ ApplyProjectModal: () => null }));
vi.mock("./mailbox-client", () => ({ MailboxClientModal: () => null }));
vi.mock("./order-panel", () => ({ OrderPanel: () => null }));
vi.mock("./product-picker-panel", () => ({ ProductPickerPanel: () => null }));
vi.mock("@douyinfe/semi-ui", () => ({
  Card: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  Tag: ({ children }: { children: ReactNode }) => <span>{children}</span>,
  Empty: () => null, Input: () => null,
  Toast: { error: mocks.toast },
}));
vi.mock("@douyinfe/semi-icons", () => ({ IconSearch: () => null }));
vi.mock("@/components/semi/overflow-tooltip", () => ({
  OverflowTooltip: ({ children }: { children: ReactNode }) => <span>{children}</span>,
}));

function project(id: number, stock: number | null): ProjectItem {
  return {
    id, name: `Project ${id}`, targetPlatform: "", status: "listed", accessType: "public",
    looseMatch: false, productCount: 1, mailRuleCount: 0, supportsDotAlias: false, supportsPlusAlias: false,
    createdAt: "2026-11-01T00:00:00Z", updatedAt: "2026-11-01T00:00:00Z",
    products: [{
      type: "microsoft", status: "enabled", codeEnabled: true, purchaseEnabled: true,
      codePrice: "1", purchasePrice: "1", priceMultiplier: "1", codeWindowMinutes: 10,
      activationWindowMinutes: 10, warrantyMinutes: 60, totalAvailable: stock, publicAvailable: stock,
    }],
  };
}

function inventory(id: number, stock: number): ProjectInventoryTotalResponse {
  return { projectId: id, totalAvailable: stock, products: [{ productType: "microsoft", totalAvailable: stock, publicAvailable: stock }] };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function row(container: HTMLElement, id: number) {
  return Array.from(container.querySelectorAll<HTMLButtonElement>(".workbench-project-row"))
    .find((item) => item.textContent?.includes(`Project ${id}`))!;
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
  mocks.orders.mockResolvedValue({ items: [], hasNext: false });
  mocks.inventory.mockImplementation((id: number) =>
    Promise.resolve(inventory(id, id === 2 ? 37 : 12)),
  );
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

it("shows cached stock immediately and fills missing list snapshots without a detail fan-out", async () => {
  mocks.inventory.mockResolvedValue(inventory(1, 12));
  mocks.list.mockResolvedValueOnce({ items: [project(1, 12), project(2, null), project(3, 0)] })
    .mockResolvedValue({ items: [project(1, 12), project(2, 37), project(3, 0)] });
  const view = await act(async () => render(<Dashboard />));
  expect(row(view.container, 1)).toHaveTextContent("Stock 12");
  expect(row(view.container, 2)).toHaveTextContent("--");
  expect(row(view.container, 2)).not.toHaveTextContent("Stock 0");
  expect(row(view.container, 3)).toHaveTextContent("Stock 0");
  expect(mocks.inventory.mock.calls.map(([id]) => id)).toEqual([1]);
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(mocks.list).toHaveBeenCalledTimes(2);
  expect(row(view.container, 2)).toHaveTextContent("Stock 37");

  view.unmount();
  const reload = await act(async () => render(<Dashboard />));
  expect(row(reload.container, 2)).toHaveTextContent("Stock 37");
});

it("shares in-flight requests across focus and polling, then cancels them on unmount", async () => {
  const detail = deferred<ProjectInventoryTotalResponse>();
  const refresh = deferred<{ items: ProjectItem[] }>();
  mocks.inventory.mockReturnValue(detail.promise);
  mocks.list.mockResolvedValueOnce({ items: [project(1, null), project(2, null)] }).mockReturnValue(refresh.promise);
  const view = await act(async () => render(<Dashboard />));
  expect(mocks.inventory).toHaveBeenCalledTimes(1);
  await act(async () => { fireEvent.focus(window); fireEvent.focus(window); });
  await act(async () => { await vi.advanceTimersByTimeAsync(30_000); });
  expect(mocks.list).toHaveBeenCalledTimes(2);
  await act(async () => { refresh.resolve({ items: [project(1, null), project(2, 5)] }); });
  expect(mocks.inventory).toHaveBeenCalledTimes(1);

  const signals = mocks.inventory.mock.calls.map(([, signal]) => signal as AbortSignal);
  expect(signals.every((signal) => !signal.aborted)).toBe(true);
  view.unmount();
  expect(signals.every((signal) => signal.aborted)).toBe(true);
  await act(async () => { detail.resolve(inventory(1, 9)); await vi.advanceTimersByTimeAsync(30_000); });
  expect(mocks.list).toHaveBeenCalledTimes(2);
  expect(mocks.toast).not.toHaveBeenCalled();
});

it("keeps the selected project's loaded stock when a later list response has no snapshot", async () => {
  mocks.inventory.mockImplementation((id: number) =>
    id === 1 ? Promise.resolve(inventory(1, 12)) : Promise.resolve({ projectId: id, totalAvailable: 0, products: [] }),
  );
  mocks.list.mockResolvedValue({ items: [project(1, null), project(2, null)] });
  const view = await act(async () => render(<Dashboard />));
  expect(row(view.container, 1)).toHaveTextContent("Stock 12");
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(row(view.container, 1)).toHaveTextContent("Stock 12");
  expect(row(view.container, 2)).toHaveTextContent("--");
});

it("does not treat an empty cold detail response as loaded stock and retries batch reads after failure", async () => {
  mocks.list.mockResolvedValueOnce({ items: [project(1, null), project(2, null)] })
    .mockRejectedValueOnce(new Error("temporary cache read failure"))
    .mockResolvedValue({ items: [project(1, 0), project(2, 15)] });
  mocks.inventory.mockResolvedValue({ projectId: 1, totalAvailable: 0, products: [] });
  const view = await act(async () => render(<Dashboard />));
  expect(row(view.container, 1)).toHaveTextContent("--");
  Object.defineProperty(document, "hidden", { configurable: true, value: true });
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(mocks.list).toHaveBeenCalledTimes(1);
  Object.defineProperty(document, "hidden", { configurable: true, value: false });
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(row(view.container, 2)).toHaveTextContent("--");
  await act(async () => { await vi.advanceTimersByTimeAsync(15_000); });
  expect(row(view.container, 1)).toHaveTextContent("Stock 0");
  expect(row(view.container, 2)).toHaveTextContent("Stock 15");
  expect(mocks.toast).not.toHaveBeenCalled();
});
