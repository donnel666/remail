// @vitest-environment jsdom

import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  copyText: vi.fn(),
  createCardKeys: vi.fn(),
  toastError: vi.fn(),
  toastSuccess: vi.fn(),
  toastWarning: vi.fn(),
}));

vi.mock("react-i18next", () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

vi.mock("@douyinfe/semi-ui", () => {
  const Button = ({ children, onClick }: any) => (
    <button onClick={onClick} type="button">
      {children}
    </button>
  );
  const DatePicker = () => <input aria-label="Expire at" />;
  const InputNumber = ({ onChange, value }: any) => (
    <input
      onChange={(event) => onChange?.(event.target.value)}
      value={value ?? ""}
    />
  );
  const Modal = ({
    cancelButtonProps,
    children,
    onCancel,
    onOk,
    okText,
    title,
    visible,
  }: any) =>
    visible ? (
      <section aria-label={title} role="dialog">
        {children}
        {cancelButtonProps?.style?.display !== "none" ? (
          <button onClick={onCancel} type="button">
            Cancel
          </button>
        ) : null}
        <button onClick={onOk} type="button">
          {okText}
        </button>
      </section>
    ) : null;
  const TextArea = ({
    "aria-label": ariaLabel,
    onChange,
    placeholder,
    readonly,
    rows,
    value,
  }: any) => (
    <textarea
      aria-label={ariaLabel}
      onChange={(event) => onChange?.(event.target.value)}
      placeholder={placeholder}
      readOnly={readonly}
      rows={rows}
      value={value ?? ""}
    />
  );
  return {
    Button,
    DatePicker,
    InputNumber,
    Modal,
    TextArea,
    Toast: {
      error: mocks.toastError,
      success: mocks.toastSuccess,
      warning: mocks.toastWarning,
    },
  };
});

vi.mock("@/lib/iam-errors", () => ({
  getIamErrorMessage: (_t: unknown, _error: unknown, fallback: string) =>
    fallback,
}));

vi.mock("./admin-finance-api", () => ({
  createFinanceCardKeys: mocks.createCardKeys,
  updateFinanceCardKey: vi.fn(),
}));

vi.mock("./finance-meta", () => ({ formatMoney: (value: string) => value }));
vi.mock("./finance-shared", () => ({ copyText: mocks.copyText }));

import { CreateCardKeyModal } from "./card-key-modals";

describe("CreateCardKeyModal", () => {
  beforeEach(() => vi.clearAllMocks());
  afterEach(() => cleanup());

  it("keeps every created card key visible and copies them one per line", async () => {
    mocks.createCardKeys.mockResolvedValue({
      created: 2,
      items: [{ key: "CDK-ONE" }, { key: "CDK-TWO" }],
    });
    const onCreated = vi.fn();
    const onOpenChange = vi.fn();
    render(
      <CreateCardKeyModal
        onCreated={onCreated}
        onOpenChange={onOpenChange}
        open
      />
    );

    fireEvent.change(screen.getByLabelText("Count"), {
      target: { value: "2" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(mocks.createCardKeys).toHaveBeenCalledWith(
        expect.objectContaining({ count: 2 })
      )
    );
    expect(await screen.findByLabelText("Card keys")).toHaveValue(
      "CDK-ONE\nCDK-TWO"
    );
    expect(onCreated).toHaveBeenCalledTimes(1);
    expect(onOpenChange).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Copy all" }));
    await waitFor(() =>
      expect(mocks.copyText).toHaveBeenCalledWith("CDK-ONE\nCDK-TWO")
    );

    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
