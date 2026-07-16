// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const toastError = vi.hoisted(() => vi.fn());
const apiMocks = vi.hoisted(() => ({
  getMessage: vi.fn(),
  listMessages: vi.fn(),
}));

vi.mock("@douyinfe/semi-icons", () => ({
  IconSearch: () => <span>search</span>,
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const Empty = ({ description }: any) => <div>{description}</div>;
  const Input = ({ onChange, placeholder, value }: any) => (
    <input
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      value={value ?? ""}
    />
  );
  const Spin = () => <div>loading</div>;
  const Tag = ({ children }: any) => <span>{children}</span>;
  const Text = ({ children }: any) => <span>{children}</span>;
  return {
    Button: ({ children, onClick }: any) => <button onClick={onClick}>{children}</button>,
    Empty,
    Input,
    SideSheet: ({ children }: any) => <section>{children}</section>,
    Spin,
    Table: () => null,
    Tabs: Object.assign(({ children }: any) => <div>{children}</div>, {
      TabPane: () => null,
    }),
    Tag,
    Toast: { error: toastError },
    Typography: { Text },
  };
});

vi.mock("@/components/semi/card-pro-pagination", () => ({
  createCardProPagination: () => null,
}));
vi.mock("@/components/semi/copyable-config", () => ({
  createCopyableConfig: () => undefined,
}));
vi.mock("@/components/semi/copyable-table-text", () => ({
  CopyableTableText: ({ text }: { text: string }) => <span>{text}</span>,
}));
vi.mock("@/hooks/use-debounced-value", () => ({
  useDebouncedValue: (value: string) => [value, vi.fn()],
}));
vi.mock("@/hooks/use-is-mobile", () => ({ useIsMobile: () => false }));
vi.mock("@/hooks/use-shared-page-size", () => ({
  useSharedPageSize: () => [20, vi.fn()],
}));
vi.mock("../orders/order-meta", () => ({
  formatLedgerAmount: (value: string) => value,
  renderOrderStatusTag: () => null,
  renderServiceModeTag: () => null,
}));
vi.mock("../workbench/project-icon", () => ({ ProjectIcon: () => null }));
vi.mock("./admin-domains-api", () => ({
  getAdminDomainMessage: apiMocks.getMessage,
  listAdminDomainMessages: apiMocks.listMessages,
}));
vi.mock("./domain-meta", () => ({
  DomainInfoItem: ({ label, value }: any) => (
    <div>
      <span>{label}</span>
      <span>{value}</span>
    </div>
  ),
  domainOwnerRoleLabel: (value: string) => value,
  formatDomainTime: (value?: string) => value ?? "-",
  renderDomainPurposeTag: () => null,
  renderDomainStatusTag: () => null,
}));

import type { AdminDomainMessage } from "./admin-domains-api";
import { DomainMailsPanel } from "./domain-detail-sheet";

const t = ((key: string) => key) as any;

function message(id: number): AdminDomainMessage {
  return {
    body: `Preview ${id}`,
    id,
    preview: `Preview ${id}`,
    receivedAt: `2026-07-16T00:00:${String(id).padStart(2, "0")}Z`,
    recipient: `user${id}@example.com`,
    sender: `sender${id}@example.net`,
    status: "received",
    subject: `Subject ${id}`,
  };
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("admin domain mailbox infinite list", () => {
  it("uses the backend total and loads the next global-size page on scroll", async () => {
    const firstPage = Array.from({ length: 20 }, (_, index) => message(index + 1));
    apiMocks.getMessage.mockImplementation(async (id) => ({
      ...message(id),
      body: `Body ${id}`,
    }));
    apiMocks.listMessages
      .mockResolvedValueOnce({
        items: firstPage,
        limit: 20,
        offset: 0,
        total: 21,
        hasMore: true,
        nextBeforeReceivedAt: firstPage[19].receivedAt,
        nextBeforeId: 20,
      })
      .mockResolvedValueOnce({
        items: [message(21)],
        limit: 20,
        offset: 0,
        hasMore: false,
      })
      .mockResolvedValueOnce({
        items: [],
        limit: 20,
        offset: 0,
        total: 0,
        hasMore: false,
      });

    render(
      <DomainMailsPanel resourceId={42} t={t} />
    );

    await waitFor(() =>
      expect(apiMocks.listMessages).toHaveBeenCalledWith(
        42,
        "",
        20,
        undefined,
        expect.any(AbortSignal)
      )
    );
    expect(await screen.findByText("Subject 20")).toBeInTheDocument();
    expect(screen.getByText("Mail count")).toBeInTheDocument();
    expect(screen.getByText("21")).toBeInTheDocument();

    const scroller = screen.getByTestId("admin-domain-message-list");
    Object.defineProperties(scroller, {
      clientHeight: { configurable: true, value: 200 },
      scrollHeight: { configurable: true, value: 400 },
      scrollTop: { configurable: true, value: 180 },
    });
    fireEvent.scroll(scroller);

    await waitFor(() =>
      expect(apiMocks.listMessages).toHaveBeenCalledWith(
        42,
        "",
        20,
        {
          beforeReceivedAt: firstPage[19].receivedAt,
          beforeId: 20,
        },
        expect.any(AbortSignal)
      )
    );
    expect(await screen.findByText("Subject 21")).toBeInTheDocument();

    fireEvent.change(
      screen.getByPlaceholderText("Search sender, recipient, subject or body"),
      { target: { value: "new scope" } }
    );
    await waitFor(() =>
      expect(apiMocks.listMessages).toHaveBeenCalledWith(
        42,
        "new scope",
        20,
        undefined,
        expect.any(AbortSignal)
      )
    );
    expect(
      apiMocks.listMessages.mock.calls.filter(([, search]) => search === "new scope")
    ).toHaveLength(1);
  });

  it("sends search to the server and replaces the previous page", async () => {
    apiMocks.listMessages
      .mockResolvedValueOnce({
        items: [message(1)],
        limit: 20,
        offset: 0,
        total: 1,
        hasMore: false,
      })
      .mockResolvedValueOnce({
        items: [message(9)],
        limit: 20,
        offset: 0,
        total: 1,
        hasMore: false,
      });

    render(
      <DomainMailsPanel resourceId={42} t={t} />
    );
    expect(await screen.findByText("Subject 1")).toBeInTheDocument();

    fireEvent.change(
      screen.getByPlaceholderText("Search sender, recipient, subject or body"),
      { target: { value: "needle" } }
    );

    await waitFor(() =>
      expect(apiMocks.listMessages).toHaveBeenCalledWith(
        42,
        "needle",
        20,
        undefined,
        expect.any(AbortSignal)
      )
    );
    expect(await screen.findByText("Subject 9")).toBeInTheDocument();
    expect(screen.queryByText("Subject 1")).not.toBeInTheDocument();
  });

  it("renders a retryable first-page error instead of an empty mailbox", async () => {
    apiMocks.listMessages
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({
        items: [message(1)],
        limit: 20,
        offset: 0,
        total: 1,
        hasMore: false,
      });

    render(<DomainMailsPanel resourceId={42} t={t} />);

    expect(await screen.findByText("Mail load failed.")).toBeInTheDocument();
    expect(screen.queryByText("No mails yet")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByText("Subject 1")).toBeInTheDocument();
    expect(apiMocks.listMessages).toHaveBeenCalledTimes(2);
  });
});
