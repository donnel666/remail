// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ButtonHTMLAttributes, ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

vi.mock("react-i18next", () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock("@douyinfe/semi-ui", () => ({
  Button: ({ children, onClick }: ButtonHTMLAttributes<HTMLButtonElement>) => <button onClick={onClick}>{children}</button>,
}));
vi.mock("./orders-trade", () => ({ default: () => null }));
vi.mock("./project-pricing", () => ({ default: () => null }));
vi.mock("./payment-billing", () => ({ default: () => null }));
vi.mock("./settings-layout", () => ({
  SettingsCardHeader: () => null,
  SettingsFormGrid: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  SettingsSection: ({ children }: { children: ReactNode }) => <section>{children}</section>,
  SettingsNumberField: ({ label, value, onChange }: { label: string; value: number; onChange: (value: number) => void }) => (
    <label>{label}<input type="number" value={value} onChange={(event) => onChange(Number(event.target.value))} /></label>
  ),
}));

import OrdersPaymentSection from "./orders-payment";

afterEach(cleanup);

it.each([undefined, "0.7"])("saves an independent enterprise multiplier with initial value %s", async (enterpriseValue) => {
  const onBulkSave = vi.fn().mockResolvedValue(undefined);
  render(<OrdersPaymentSection
    canReadUserGroups canSensitive canWrite canWriteUserGroups loading={false}
    onSave={vi.fn()} onBulkSave={onBulkSave}
    options={[
      { key: "product_price_multiplier_microsoft", value: "0.8" },
      { key: "product_price_multiplier_gmail", value: "0.9" },
      ...(enterpriseValue ? [{ key: "product_price_multiplier_microsoft_enterprise", value: enterpriseValue }] : []),
    ]}
  />);

  expect(screen.getByLabelText("Outlook 倍率")).toHaveValue(0.8);
  expect(screen.getByLabelText("微软企邮倍率")).toHaveValue(Number(enterpriseValue ?? "1"));
  fireEvent.change(screen.getByLabelText("微软企邮倍率"), { target: { value: "0.6" } });
  expect(screen.getByLabelText("Outlook 倍率")).toHaveValue(0.8);
  fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

  await waitFor(() => expect(onBulkSave).toHaveBeenCalledWith([
    { key: "product_price_multiplier_microsoft", value: "0.8" },
    { key: "product_price_multiplier_microsoft_enterprise", value: "0.6" },
    { key: "product_price_multiplier_gmail", value: "0.9" },
    { key: "product_price_multiplier_icloud", value: "1" },
    { key: "product_price_multiplier_domain", value: "1" },
    { key: "product_price_multiplier_proto", value: "1" },
  ]));
});
