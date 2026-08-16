// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
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
vi.mock("./settings-layout", () => ({
  SettingsCardHeader: ({ title }: { title: string }) => <span>{title}</span>,
  SettingsSection: ({ children, title }: any) => <section><h2>{title}</h2>{children}</section>,
}));

vi.mock("@douyinfe/semi-ui", async () => {
  const React = await import("react");
  const Button = ({ children, icon: _icon, loading: _loading, onClick, theme: _theme, type: _type, ...props }: any) => (
    <button onClick={onClick} type="button" {...props}>{children}</button>
  );
  const Modal = ({ children, footer, onOk, okText, title, visible }: any) => visible ? (
    <div aria-label={title} role="dialog">
      <h3>{title}</h3>
      {children}
      {onOk ? <button onClick={onOk} type="button">{okText}</button> : null}
      {footer}
    </div>
  ) : null;
  Modal.confirm = vi.fn();
  return {
    Button,
    Empty: ({ description }: any) => <div>{description}</div>,
    Input: ({ onChange, prefix: _prefix, showClear: _showClear, ...props }: any) => <input onChange={(event) => onChange(event.target.value)} {...props} />,
    Modal,
    Table: ({ columns, dataSource }: any) => <div>{dataSource.map((item: any) => <div key={item.id}>{columns.map((column: any, index: number) => <React.Fragment key={index}>{column.render ? column.render(item[column.dataIndex], item) : item[column.dataIndex]}</React.Fragment>)}</div>)}</div>,
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
      items: [{ id: 1, name: "Existing", keyPrefix: "sk_existing", keyPlain: undefined, lastUsedAt: null, createdAt: "2026-08-16T00:00:00Z" }],
    });
    mocks.create.mockResolvedValue({
      id: 2, name: "Worker", keyPrefix: "sk_created", keyPlain: "sk_created_secret", lastUsedAt: null, createdAt: "2026-08-16T01:00:00Z",
    });
  });

  afterEach(() => cleanup());

  it("shows a newly created key once and keeps only its prefix in the list", async () => {
    render(<SystemKeysSection {...props} />);

    expect(await screen.findByText("Existing")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create system key" }));
    fireEvent.change(screen.getByLabelText("System key name"), { target: { value: "Worker" } });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByText("sk_created_secret")).toBeInTheDocument();
    expect(mocks.create).toHaveBeenCalledWith("Worker");
    fireEvent.click(screen.getByRole("button", { name: "I copied the key" }));

    await waitFor(() => expect(screen.queryByText("sk_created_secret")).not.toBeInTheDocument());
    expect(screen.getByText("sk_created...")).toBeInTheDocument();
  });
});
