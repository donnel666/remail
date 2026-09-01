// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  confirm: vi.fn(),
  create: vi.fn(),
  list: vi.fn(),
  remove: vi.fn(),
}));
const translate = (key: string, options?: { name?: string }) => options?.name ? key.replace("{{name}}", options.name) : key;

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: translate,
  }),
}));

vi.mock("@/lib/system-keys-api", () => ({
  createSystemKey: mocks.create,
  deleteSystemKey: mocks.remove,
  listSystemKeys: mocks.list,
}));

vi.mock("@/lib/iam-errors", () => ({ getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) => fallback }));
vi.mock("@/components/semi/copyable-config", () => ({ createCopyableConfig: () => undefined }));
vi.mock("./settings-layout", async () => {
  const actual = await vi.importActual<typeof import("./settings-layout")>("./settings-layout");
  return {
    ...actual,
    SettingsSection: ({ children, title }: any) => <section><h2>{title}</h2>{children}</section>,
  };
});

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Button = ({ children, icon: _icon, loading: _loading, onClick, theme: _theme, type: _type, ...props }: any) => (
    <button onClick={onClick} type="button" {...props}>{children}</button>
  );
  const Modal = ({ cancelText, children, footer, onCancel, onOk, okText, title, visible, width }: any) => visible ? (
    <div aria-label={title} data-width={width} role="dialog">
      <h3>{title}</h3>
      {children}
      {onOk ? <button onClick={onOk} type="button">{okText}</button> : null}
      {onCancel ? <button onClick={onCancel} type="button">{cancelText}</button> : null}
      {footer}
    </div>
  ) : null;
  Modal.confirm = mocks.confirm;
  return {
    Card: ({ children }: any) => <div>{children}</div>,
    Button,
    Input: ({ onChange, prefix: _prefix, showClear: _showClear, ...props }: any) => <input onChange={(event) => onChange(event.target.value)} {...props} />,
    TextArea: ({ autosize: _autosize, onChange, ...props }: any) => <textarea onChange={(event) => onChange(event.target.value)} {...props} />,
    Modal,
    Radio: ({ children, value }: any) => <label><input type="radio" value={value} />{children}</label>,
    RadioGroup: ({ children, onChange }: any) => <div onChange={onChange}>{children}</div>,
    Select: ({ onChange, optionList, value, ...props }: any) => (
      <select onChange={(event) => onChange(event.target.value)} value={value} {...props}>
        {optionList.map((option: any) => <option key={option.value} value={option.value}>{option.label}</option>)}
      </select>
    ),
    Space: ({ children }: any) => <div>{children}</div>,
    Switch: ({ checked, onChange }: any) => <input checked={checked} onChange={(event) => onChange(event.target.checked)} type="checkbox" />,
    Tag: ({ children }: any) => <span>{children}</span>,
    Toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
    Tooltip: ({ children }: any) => <>{children}</>,
    Typography: { Text: ({ children }: any) => <span>{children}</span> },
  };
});

import SystemKeysSection from "./system-keys";

const props = {
  canReadUserGroups: false,
  canSensitive: true,
  canWrite: true,
  canWriteUserGroups: false,
  loading: false,
  onBulkSave: vi.fn(),
  onSave: vi.fn(),
  options: [],
};

describe("SystemKeysSection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.list.mockResolvedValue({
      items: [{ id: 1, name: "Existing", purpose: "smtp_submission", keyPrefix: "sk_existing", keyPlain: undefined, lastUsedAt: null, createdAt: "2026-08-16T00:00:00Z" }],
    });
    mocks.create.mockResolvedValue({
      id: 2, name: "Worker", purpose: "smtp_submission", keyPrefix: "sk_created", keyPlain: "sk_created_secret", lastUsedAt: null, createdAt: "2026-08-16T01:00:00Z",
    });
  });

  afterEach(() => cleanup());

  it("shows a newly created key once and keeps only its prefix in the list", async () => {
    render(<SystemKeysSection {...props} />);

    expect(await screen.findByText("Existing")).toBeInTheDocument();
    expect(screen.queryByText("Bot scope")).not.toBeInTheDocument();
    expect(screen.queryByText("Allowed groups")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    expect(screen.getByRole("dialog", { name: "Create system key" })).toHaveAttribute(
      "data-width",
      "min(448px, calc(100vw - 32px))",
    );
    fireEvent.change(screen.getByLabelText("System key name"), { target: { value: "Worker" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("sk_created_secret")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "System key created" })).toHaveAttribute(
      "data-width",
      "min(448px, calc(100vw - 32px))",
    );
    const warning = screen.getByRole("alert");
    expect(warning).toHaveTextContent("System key shown once warning");
    expect(within(warning).queryByText("sk_created_secret")).not.toBeInTheDocument();
    expect(mocks.create).toHaveBeenCalledWith("Worker", "smtp_submission");
    fireEvent.click(screen.getByRole("button", { name: "I copied the key" }));

    await waitFor(() => expect(screen.queryByText("sk_created_secret")).not.toBeInTheDocument());
    expect(screen.getByText("sk_created...")).toBeInTheDocument();
  });

  it("requires and submits the scope for a bot key", async () => {
    mocks.create.mockResolvedValueOnce({
      id: 3,
      name: "QQ Bot",
      purpose: "bot",
      platform: "qq",
      subjectNamespace: "qq:main",
      allowedGroupIds: ["123456", "234567", "345678"],
      keyPrefix: "sk_bot",
      keyPlain: "sk_bot_secret",
      lastUsedAt: null,
      createdAt: "2026-08-16T02:00:00Z",
    });
    render(<SystemKeysSection {...props} />);

    await screen.findByText("Existing");
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    fireEvent.click(screen.getByLabelText("Bot integration"));
    fireEvent.change(screen.getByLabelText("System key name"), { target: { value: "QQ Bot" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(mocks.create).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Allowed group IDs"), { target: { value: "123456, 234567，345678、234567" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith("QQ Bot", "bot", {
      platform: "qq",
      subjectNamespace: "qq:main",
      allowedGroupIds: ["123456", "234567", "345678"],
    }));
    expect(screen.getByText("123456, 234567, 345678")).toBeInTheDocument();
  });

  it("maps the Telegram robot type to its own key scope", async () => {
    render(<SystemKeysSection {...props} />);

    await screen.findByText("Existing");
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    fireEvent.click(screen.getByLabelText("Bot integration"));
    fireEvent.change(screen.getByLabelText("Robot type"), { target: { value: "telegram" } });
    fireEvent.change(screen.getByLabelText("System key name"), { target: { value: "TG Bot" } });
    fireEvent.change(screen.getByLabelText("Allowed group IDs"), { target: { value: "-1001234567890" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith("TG Bot", "bot", {
      platform: "telegram",
      subjectNamespace: "telegram:main",
      allowedGroupIds: ["-1001234567890"],
    }));
  });

  it("resets the creation form after cancellation", async () => {
    render(<SystemKeysSection {...props} />);

    await screen.findByText("Existing");
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    fireEvent.change(screen.getByLabelText("System key name"), { target: { value: "Draft" } });
    fireEvent.click(screen.getByLabelText("Bot integration"));
    fireEvent.change(screen.getByLabelText("Allowed group IDs"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    expect(screen.queryByRole("dialog", { name: "Create system key" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    expect(screen.getByLabelText("System key name")).toHaveValue("");
    expect(screen.queryByLabelText("Allowed group IDs")).not.toBeInTheDocument();
  });

  it("revokes a key through the confirmation flow", async () => {
    render(<SystemKeysSection {...props} />);

    expect(await screen.findByText("Existing")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Revoke system key" }));
    expect(mocks.confirm).toHaveBeenCalledOnce();

    await mocks.confirm.mock.calls[0][0].onOk();

    expect(mocks.remove).toHaveBeenCalledWith(1);
    await waitFor(() => expect(screen.queryByText("Existing")).not.toBeInTheDocument());
  });

  it("renders bot key scope and groups in the responsive key summary", async () => {
    mocks.list.mockResolvedValueOnce({
      items: [{
        id: 4,
        name: "QQ Support Bot",
        purpose: "bot",
        platform: "qq",
        subjectNamespace: "qq:main",
        allowedGroupIds: ["123456789", "987654321"],
        keyPrefix: "sk_support",
        lastUsedAt: null,
        createdAt: "2026-08-16T00:00:00Z",
      }],
    });

    render(<SystemKeysSection {...props} />);

    expect(await screen.findByText("QQ Support Bot")).toBeInTheDocument();
    expect(screen.getByText("sk_support...")).toBeInTheDocument();
    expect(screen.getByText("qq / qq:main")).toBeInTheDocument();
    expect(screen.getByText("123456789, 987654321")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Revoke system key" })).toHaveClass(
      "!h-11",
      "!w-11",
      "sm:!h-8",
      "sm:!w-8",
    );
  });
});
