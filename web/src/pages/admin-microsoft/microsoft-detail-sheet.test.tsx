// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminMicrosoftResourceDetail } from "./admin-microsoft-types";

const mocks = vi.hoisted(() => ({
  alias: vi.fn(),
  allocationPage: vi.fn(),
  bindingMessage: vi.fn(),
  bindingMessages: vi.fn(),
  createAlias: vi.fn(),
  fetchMail: vi.fn(),
  message: vi.fn(),
  messages: vi.fn(),
  mobile: false,
  refreshToken: vi.fn(),
  tasks: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  validate: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-icons", () => ({
  IconSearch: () => <span>search</span>,
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const ReactModule = await import("react");
  const Button = ({ children, disabled, loading, onClick }: any) => (
    <button data-loading={loading ? "true" : "false"} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  );
  const Empty = ({ description }: any) => <div>{description}</div>;
  const Input = ({ onChange, placeholder, value }: any) => (
    <input
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={Array.isArray(placeholder) ? placeholder.join(" / ") : placeholder}
      value={value ?? ""}
    />
  );
  const Select = ({ onChange, optionList = [], value }: any) => (
    <select onChange={(event) => onChange?.(event.target.value)} value={value ?? ""}>
      {optionList.map((option: any) => (
        <option key={String(option.value)} value={option.value}>
          {option.label}
        </option>
      ))}
    </select>
  );
  const SideSheet = ({ children, title, visible, width }: any) =>
    visible ? (
      <section data-testid="side-sheet" data-width={String(width)}>
        <h1>{title}</h1>
        {children}
      </section>
    ) : null;
  const Space = ({ children }: any) => <div>{children}</div>;
  const Spin = () => <div>loading</div>;
  const Table = ({ dataSource = [] }: any) => (
    <div data-testid="table" data-rows={String(dataSource.length)} />
  );
  const TabPane = () => null;
  const Tabs = ({ children, onChange }: any) => (
    <div role="tablist">
      {ReactModule.Children.map(children, (child) =>
        ReactModule.isValidElement(child) ? (
          <button
            onClick={() => onChange?.((child.props as any).itemKey)}
            role="tab"
            type="button"
          >
            {(child.props as any).tab}
          </button>
        ) : null
      )}
    </div>
  );
  (Tabs as any).TabPane = TabPane;
  const Tag = ({ children }: any) => <span>{children}</span>;
  const Text = ({ children }: any) => <span>{children}</span>;
  return {
    Button,
    Empty,
    Input,
    Select,
    SideSheet,
    Space,
    Spin,
    Table,
    Tabs,
    Tag,
    Toast: { error: mocks.toastError, success: mocks.toastSuccess },
    Typography: { Text },
  };
});

vi.mock("@/components/semi/card-pro-pagination", () => ({
  createCardProPagination: () => <div>pagination</div>,
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

vi.mock("@/hooks/use-is-mobile", () => ({
  useIsMobile: () => mocks.mobile,
}));

vi.mock("@/hooks/use-shared-page-size", () => ({
  useSharedPageSize: () => [20, vi.fn()],
}));

vi.mock("./use-admin-microsoft-allocation-page", () => ({
  useAdminMicrosoftAllocationPage: (...args: unknown[]) => mocks.allocationPage(...args),
}));

vi.mock("../orders/order-meta", () => ({
  formatLedgerAmount: (value: string) => value,
  renderOrderStatusTag: (value: string) => <span>{value}</span>,
  renderServiceModeTag: (value: string) => <span>{value}</span>,
}));

vi.mock("../workbench/project-icon", () => ({
  ProjectIcon: ({ name }: { name: string }) => <span>{name}</span>,
}));

vi.mock("./microsoft-meta", () => ({
  ALLOCATION_STATUS_META: {
    allocated: { label: "Allocated" },
    released: { label: "Released" },
  },
  ConfiguredTag: ({ configured }: { configured: boolean }) => (
    <span>{configured ? "configured" : "missing"}</span>
  ),
  DRAWER_PANEL_HEIGHT: "360px",
  DRAWER_TABLE_SCROLL_Y: "220px",
  InfoItem: ({ label, value }: any) => (
    <div>
      <span>{label}</span>
      <span>{value}</span>
    </div>
  ),
  MAILBOX_META: {
    alias: { label: "Explicit alias" },
    dot: { label: "Dot alias" },
    main: { label: "Main mailbox" },
    plus: { label: "Plus alias" },
  },
  MAILBOX_TEXT_COLOR: { alias: "", dot: "", main: "", plus: "" },
  OwnerIdentity: ({ owner }: any) => <span>{owner.email}</span>,
  SUPPLY_SCOPE_META: { owned: { label: "Owned" }, public: { label: "Public" } },
  formatRemainingTime: () => "-",
  formatTime: (value?: string) => value ?? "-",
  renderMailboxTag: (value: string) => <span>{value}</span>,
  renderMessageStatusTag: (value: string) => <span>{value}</span>,
  renderProtocolTag: (value: { mailProtocol?: string }) => (
    <span>{value.mailProtocol ?? "unavailable"}</span>
  ),
  renderStatusTag: (value: string) => <span>{value}</span>,
  renderTaskStatusTag: (value: string) => <span>{value}</span>,
  renderTokenTag: (value: string) => <span>{value}</span>,
  taskKindLabel: (value: string) => value,
}));

vi.mock("@/lib/admin-microsoft-api", () => ({
  createAdminMicrosoftExplicitAlias: mocks.createAlias,
  fetchAdminMicrosoftMail: mocks.fetchMail,
  getAdminMicrosoftBindingMessage: mocks.bindingMessage,
  getAdminMicrosoftMessage: mocks.message,
  listAdminMicrosoftAliases: mocks.alias,
  listAdminMicrosoftBindingMessages: mocks.bindingMessages,
  listAdminMicrosoftMessages: mocks.messages,
  listAdminMicrosoftTasks: mocks.tasks,
  refreshAdminMicrosoftToken: mocks.refreshToken,
  validateAdminMicrosoftResource: mocks.validate,
}));

import { MicrosoftDetailSheet } from "./microsoft-detail-sheet";

const EMPTY_PAGE = {
  items: [],
  limit: 20,
  offset: 0,
  total: 0,
  hasMore: false,
};
const EMPTY_TASKS = { ...EMPTY_PAGE, succeeded: 0 };

function detail(id: number): AdminMicrosoftResourceDetail {
  const now = "2026-07-12T00:00:00Z";
  return {
    activeTask: null,
    aliasCounts: { dot: 0, explicit: 0, plus: 0 },
    bindingAddress: `aux-${id}@example.net`,
    createdAt: now,
    credentials: {
      clientIdConfigured: true,
      passwordConfigured: true,
      refreshTokenConfigured: true,
      revision: 2,
      updatedAt: now,
    },
    emailAddress: `resource-${id}@outlook.com`,
    forSale: false,
    graphAvailable: false,
    id,
    lastAllocatedAt: null,
    lastSafeError: null,
    longLived: true,
    mailProtocol: "imap",
    owner: {
      email: "owner@example.com",
      enabled: true,
      groupName: "Supply",
      id: 7,
      nickname: "Owner",
      role: "supplier",
    },
    qualityScore: 80,
	proxyBindings: [
	  {
	    country: "US",
	    expireAt: now,
	    host: "proxy.example.net",
	    ipVersion: "ipv4",
	    outboundIp: "203.0.113.10",
	    proxyId: 12,
	    status: "normal",
	  },
	],
    recentTasks: [],
    rtExpireAt: null,
    status: "normal",
    suffix: "@outlook.com",
    token: {
      health: "valid",
      lastRefreshRequestId: null,
      lastRefreshedAt: null,
      lastSafeError: null,
      remainingSeconds: null,
      rtExpireAt: null,
      scopes: [],
    },
    tokenHealth: "valid",
    type: "microsoft",
    updatedAt: now,
    version: 3,
  };
}

function renderSheet(resource: AdminMicrosoftResourceDetail, onRefresh = vi.fn()) {
  return render(
    <MicrosoftDetailSheet
      busy={false}
      detail={resource}
      loading={false}
      onCancel={vi.fn()}
      onDelete={vi.fn()}
      onEdit={vi.fn()}
      onRecover={vi.fn()}
      onRefresh={onRefresh}
      onReplaceCredentials={vi.fn()}
      onToggleDisabled={vi.fn()}
      onTogglePublish={vi.fn()}
      onValidate={vi.fn()}
    />
  );
}

describe("admin Microsoft detail sheet runtime", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.mobile = false;
    mocks.alias.mockResolvedValue({ ...EMPTY_PAGE, schedule: null });
    mocks.tasks.mockResolvedValue(EMPTY_TASKS);
    mocks.messages.mockResolvedValue(EMPTY_PAGE);
    mocks.bindingMessages.mockResolvedValue(EMPTY_PAGE);
    mocks.message.mockResolvedValue({});
    mocks.bindingMessage.mockResolvedValue({});
    mocks.validate.mockResolvedValue({});
    mocks.refreshToken.mockResolvedValue({});
    mocks.createAlias.mockResolvedValue({});
    mocks.fetchMail.mockResolvedValue({});
    mocks.allocationPage.mockReturnValue({
      items: [],
      loading: false,
      page: 1,
      pageSize: 20,
      setPage: vi.fn(),
      setPageSize: vi.fn(),
      total: 0,
    });
  });

  afterEach(() => cleanup());

  it("keeps all seven tabs lazy and invokes each fact-owner loader", async () => {
    renderSheet(detail(41));

    expect(screen.getByTestId("side-sheet")).toHaveAttribute("data-width", "940");
    expect(screen.getAllByRole("tab").map((tab) => tab.textContent)).toEqual([
      "Basic info",
      "Orders",
      "Explicit aliases",
      "Other aliases",
      "Task details",
      "Mailbox",
      "Auxiliary mailbox",
    ]);

    fireEvent.click(screen.getByRole("tab", { name: "Orders" }));
    await waitFor(() => expect(mocks.allocationPage).toHaveBeenCalledWith(41));

    fireEvent.click(screen.getByRole("tab", { name: "Explicit aliases" }));
    await waitFor(() =>
      expect(mocks.alias).toHaveBeenCalledWith(41, "explicit", 0, 20, expect.any(AbortSignal))
    );

    fireEvent.click(screen.getByRole("tab", { name: "Other aliases" }));
    await waitFor(() =>
      expect(mocks.alias).toHaveBeenCalledWith(41, "other", 0, 20, expect.any(AbortSignal))
    );

    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await waitFor(() =>
      expect(mocks.tasks).toHaveBeenCalledWith(41, 0, 20, expect.any(AbortSignal))
    );

    fireEvent.click(screen.getByRole("tab", { name: "Mailbox" }));
    await waitFor(() =>
      expect(mocks.messages).toHaveBeenCalledWith(
        41,
        "",
        20,
        undefined,
        expect.any(AbortSignal)
      )
    );

    fireEvent.click(screen.getByRole("tab", { name: "Auxiliary mailbox" }));
    await waitFor(() =>
      expect(mocks.bindingMessages).toHaveBeenCalledWith(
        41,
        "",
        20,
        undefined,
        expect.any(AbortSignal)
      )
    );
  });

  it("uses the backend mail count and global page size for infinite scroll", async () => {
    const mail = (id: number) => ({
      id,
      mailbox: "main",
      orderNo: null,
      preview: `Preview ${id}`,
      receivedAt: `2026-07-12T00:00:${String(id).padStart(2, "0")}Z`,
      recipient: `user${id}@outlook.com`,
      sender: `sender${id}@example.net`,
      status: "received",
      subject: `Message ${id}`,
      verificationCode: null,
    });
    mocks.messages
      .mockResolvedValueOnce({
        items: Array.from({ length: 20 }, (_, index) => mail(index + 1)),
        limit: 20,
        offset: 0,
        total: 21,
        hasMore: true,
        nextBeforeReceivedAt: mail(20).receivedAt,
        nextBeforeId: 20,
      })
      .mockResolvedValueOnce({
        items: [mail(21)],
        limit: 20,
        offset: 0,
        hasMore: false,
      });

    renderSheet(detail(41));
    fireEvent.click(screen.getByRole("tab", { name: "Mailbox" }));

    expect(await screen.findByText("Message 20")).toBeInTheDocument();
    expect(screen.getByText("Mail count")).toBeInTheDocument();
    expect(screen.getByText("21")).toBeInTheDocument();

    const scroller = screen.getByTestId("admin-microsoft-message-list");
    Object.defineProperties(scroller, {
      clientHeight: { configurable: true, value: 200 },
      scrollHeight: { configurable: true, value: 400 },
      scrollTop: { configurable: true, value: 180 },
    });
    fireEvent.scroll(scroller);

    await waitFor(() =>
      expect(mocks.messages).toHaveBeenCalledWith(
        41,
        "",
        20,
        {
          beforeReceivedAt: mail(20).receivedAt,
          beforeId: 20,
        },
        expect.any(AbortSignal)
      )
    );
    expect(await screen.findByText("Message 21")).toBeInTheDocument();
  });

  it("retries a failed continuation with the same cursor", async () => {
    const mail = (id: number) => ({
      id,
      mailbox: "main",
      orderNo: null,
      preview: `Preview ${id}`,
      receivedAt: `2026-07-12T00:00:${String(id).padStart(2, "0")}Z`,
      recipient: `user${id}@outlook.com`,
      sender: `sender${id}@example.net`,
      status: "received",
      subject: `Message ${id}`,
      verificationCode: null,
    });
    const cursor = {
      beforeReceivedAt: mail(1).receivedAt,
      beforeId: 1,
    };
    mocks.messages
      .mockResolvedValueOnce({
        items: [mail(1)],
        limit: 20,
        offset: 0,
        total: 2,
        hasMore: true,
        nextBeforeReceivedAt: cursor.beforeReceivedAt,
        nextBeforeId: cursor.beforeId,
      })
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({
        items: [mail(2)],
        limit: 20,
        offset: 0,
        hasMore: false,
      });

    renderSheet(detail(41));
    fireEvent.click(screen.getByRole("tab", { name: "Mailbox" }));
    expect(await screen.findByText("Message 1")).toBeInTheDocument();

    const scroller = screen.getByTestId("admin-microsoft-message-list");
    Object.defineProperties(scroller, {
      clientHeight: { configurable: true, value: 200 },
      scrollHeight: { configurable: true, value: 400 },
      scrollTop: { configurable: true, value: 180 },
    });
    fireEvent.scroll(scroller);

    expect(await screen.findByRole("button", { name: "Try again" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Message 2")).toBeInTheDocument();
    expect(mocks.messages.mock.calls[1]?.[3]).toEqual(cursor);
    expect(mocks.messages.mock.calls[2]?.[3]).toEqual(cursor);
  });

  it("shows safe proxy binding details and the binding expiry in basic info", () => {
    renderSheet(detail(41));

    expect(
      screen.getByText(
        (_content, element) =>
          element?.tagName === "DIV" &&
          element.querySelector("div") === null &&
          element.textContent === "#12 proxy.example.net · ipv4 · normal · 203.0.113.10"
      )
    ).toBeInTheDocument();
    expect(
      screen.getByText(
        (_content, element) =>
          element?.tagName === "DIV" &&
          element.querySelector("div") === null &&
          element.textContent === "ipv4: 2026-07-12T00:00:00Z"
      )
    ).toBeInTheDocument();
  });

  it("keeps all four manual task actions connected and refreshes after acceptance", async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined);
    renderSheet(detail(42), onRefresh);
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await waitFor(() => expect(mocks.tasks).toHaveBeenCalled());

    const actions = [
      ["Validate", mocks.validate],
      ["Refresh RT", mocks.refreshToken],
      ["Create explicit alias", mocks.createAlias],
      ["Mail fetch", mocks.fetchMail],
    ] as const;
    for (const [index, [label, action]] of actions.entries()) {
      fireEvent.click(screen.getByRole("button", { name: label }));
      await waitFor(() => expect(action).toHaveBeenCalledWith(42));
      await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(index + 1));
    }
  });

  it("polls active tasks and stops after the task reaches a terminal state", async () => {
    vi.useFakeTimers();
    try {
      mocks.tasks
        .mockResolvedValueOnce({
          ...EMPTY_TASKS,
          items: [{ status: "running", taskId: "validation:42" }],
          total: 1,
        })
        .mockResolvedValueOnce({
          ...EMPTY_TASKS,
          items: [{ status: "succeeded", taskId: "validation:42" }],
          succeeded: 1,
          total: 1,
        });
      renderSheet(detail(42));
      fireEvent.click(screen.getByRole("tab", { name: "Task details" }));

      await act(async () => {
        await Promise.resolve();
      });
      expect(mocks.tasks).toHaveBeenCalledTimes(1);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_500);
      });
      expect(mocks.tasks).toHaveBeenCalledTimes(2);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_500);
      });
      expect(mocks.tasks).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops task polling when the task tab is left", async () => {
    vi.useFakeTimers();
    try {
      mocks.tasks.mockResolvedValueOnce({
        ...EMPTY_TASKS,
        items: [{ status: "queued", taskId: "validation:43" }],
        total: 1,
      });
      renderSheet(detail(43));
      fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
      await act(async () => {
        await Promise.resolve();
      });
      expect(mocks.tasks).toHaveBeenCalledTimes(1);

      fireEvent.click(screen.getByRole("tab", { name: "Basic info" }));
      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_500);
      });
      expect(mocks.tasks).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("aborts stale tab requests when the selected resource changes", async () => {
    let firstSignal: AbortSignal | undefined;
    mocks.tasks.mockImplementationOnce(
      (_resourceId: number, _offset: number, _limit: number, signal: AbortSignal) => {
        firstSignal = signal;
        return new Promise((_resolve, reject) => {
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("The operation was aborted.", "AbortError")),
            { once: true }
          );
        });
      }
    );
    const view = renderSheet(detail(51));
    fireEvent.click(screen.getByRole("tab", { name: "Task details" }));
    await waitFor(() => expect(firstSignal).toBeDefined());

    view.rerender(
      <MicrosoftDetailSheet
        busy={false}
        detail={detail(52)}
        loading={false}
        onCancel={vi.fn()}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onRecover={vi.fn()}
        onRefresh={vi.fn()}
        onReplaceCredentials={vi.fn()}
        onToggleDisabled={vi.fn()}
        onTogglePublish={vi.fn()}
        onValidate={vi.fn()}
      />
    );

    await waitFor(() => expect(firstSignal?.aborted).toBe(true));
    expect(screen.getByRole("tab", { name: "Basic info" })).toBeInTheDocument();
  });

  it("uses the existing full-width mobile sheet without changing desktop width", () => {
    const view = renderSheet(detail(61));
    expect(screen.getByTestId("side-sheet")).toHaveAttribute("data-width", "940");

    mocks.mobile = true;
    view.rerender(
      <MicrosoftDetailSheet
        busy={false}
        detail={detail(61)}
        loading={false}
        onCancel={vi.fn()}
        onDelete={vi.fn()}
        onEdit={vi.fn()}
        onRecover={vi.fn()}
        onRefresh={vi.fn()}
        onReplaceCredentials={vi.fn()}
        onToggleDisabled={vi.fn()}
        onTogglePublish={vi.fn()}
        onValidate={vi.fn()}
      />
    );
    expect(screen.getByTestId("side-sheet")).toHaveAttribute("data-width", "100%");
  });
});
